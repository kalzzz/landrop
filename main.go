package main

import (
	"bufio"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	DiscPort   = 45678
	XferPort   = 45679
	BufSz      = 64 * 1024
	ChunkSz    = 1024 * 1024
	MaxWorkers = 8
	ProtoV2    = "PROTOCOL_V2"
)

type Peer struct {
	ID, Name, IP string
	Port         int
	LastSeen     time.Time
}

type Config struct {
	DeviceName, SavePath string
	IsServer, EnableParallel bool
}

var (
	cfg      Config
	peers    = make(map[string]*Peer)
	peersMut sync.RWMutex
	stopCh   = make(chan bool)
)

func init() {
	h, _ := os.Hostname()
	flag.StringVar(&cfg.DeviceName, "name", h, "Device name")
	flag.StringVar(&cfg.SavePath, "path", "./downloads", "Save path")
	flag.BoolVar(&cfg.IsServer, "server", false, "Run as receiver")
	flag.BoolVar(&cfg.EnableParallel, "parallel", true, "Enable parallel transfer")
}

func main() {
	flag.Parse()
	if err := os.MkdirAll(cfg.SavePath, 0755); err != nil {
		cfg.SavePath = os.TempDir()
	}
	log.SetFlags(0)
	log.SetPrefix("[" + cfg.DeviceName + "] ")
	fmt.Printf(`
╔═══════════════════════════════════════════╗
║          LANDrop - Go 实现               ║
║        局域网点对点文件传输              ║
╚═══════════════════════════════════════════╝
设备: %s  路径: %s
`, cfg.DeviceName, cfg.SavePath)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n👋 再见!")
		stopCh <- true
		os.Exit(0)
	}()
	go discServer()
	if cfg.IsServer {
		xferServer()
	} else {
		clientMode()
	}
}

func discServer() {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: DiscPort})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	log.Printf("🌐 发现服务 (端口 %d)", DiscPort)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	broadcastPresence(c)
	buf := make([]byte, 1024)
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			broadcastPresence(c)
		default:
		}
		c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if n, addr, err := c.ReadFromUDP(buf); err == nil {
			handleDiscMsg(c, addr, buf[:n])
		} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			peersMut.Lock()
			now := time.Now()
			for id, p := range peers {
				if now.Sub(p.LastSeen) > 15*time.Second {
					delete(peers, id)
				}
			}
			peersMut.Unlock()
		}
	}
}

func broadcastPresence(c *net.UDPConn) {
	ip := getOutboundIP()
	msg := fmt.Sprintf("LANDROP_RESPONSE|%s|%s|%d", cfg.DeviceName, ip, XferPort)
	addrs := []string{"255.255.255.255:45678"}
	if p := strings.Split(ip, "."); len(p) == 4 {
		addrs = append(addrs, fmt.Sprintf("%s.%s.%s.255:45678", p[0], p[1], p[2]))
	}
	for _, a := range addrs {
		if udpAddr, err := net.ResolveUDPAddr("udp4", a); err == nil {
			c.WriteToUDP([]byte(msg), udpAddr)
		}
	}
}

func handleDiscMsg(c *net.UDPConn, addr *net.UDPAddr, data []byte) {
	parts := strings.Split(string(data), "|")
	if len(parts) < 2 {
		return
	}
	if parts[0] == "LANDROP_DISCOVER" && cfg.IsServer {
		c.WriteToUDP([]byte(fmt.Sprintf("LANDROP_RESPONSE|%s|%s|%d", cfg.DeviceName, getOutboundIP(), XferPort)), addr)
	} else if parts[0] == "LANDROP_RESPONSE" && len(parts) >= 4 && parts[2] != getOutboundIP() {
		var port int
		fmt.Sscanf(parts[3], "%d", &port)
		id := fmt.Sprintf("%s:%d", parts[2], port)
		peersMut.Lock()
		peers[id] = &Peer{ID: id, Name: parts[1], IP: parts[2], Port: port, LastSeen: time.Now()}
		peersMut.Unlock()
	}
}

func xferServer() {
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{Port: XferPort})
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	log.Printf("📁 传输服务 (端口 %d)", XferPort)
	log.Printf("💡 等待文件...")
	for {
		select {
		case <-stopCh:
			return
		default:
		}
		l.SetDeadline(time.Now().Add(1 * time.Second))
		if conn, err := l.AcceptTCP(); err == nil {
			go handleConn(conn)
		}
	}
}

func handleConn(conn *net.TCPConn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	conn.SetKeepAlive(true)
	conn.SetKeepAlivePeriod(30 * time.Second)
	r := bufio.NewReader(conn)
	vLine, _ := r.ReadString('\n')
	v2 := strings.TrimSpace(vLine) == ProtoV2
	var fname string
	var fsize int64
	var totalBlocks int
	var fcs string
	if v2 {
		var nameLen int
		fmt.Fscan(r, &nameLen)
		r.ReadByte()
		fb, _ := r.ReadBytes('\n')
		fname = strings.TrimSuffix(string(fb), "\n")
		fmt.Fscan(r, &fsize)
		fmt.Fscan(r, &totalBlocks)
		fmt.Fscan(r, &fcs)
		r.ReadByte()
		fmt.Printf("\n📥 收到: %s (%s) [V2, %d blocks]\n", fname, fs(fsize), totalBlocks)
	} else {
		var nameLen int
		fmt.Sscan(vLine, &nameLen)
		r.ReadByte()
		fb, _ := r.ReadBytes('\n')
		fname = strings.TrimSuffix(string(fb), "\n")
		fmt.Fscan(r, &fsize)
		fmt.Printf("\n📥 收到: %s (%s)\n", fname, fs(fsize))
	}
	fmt.Print("> 是否接收? [Y/n]: ")
	conn.SetDeadline(time.Time{})
	if resp, _ := bufio.NewReader(os.Stdin).ReadString('\n'); strings.TrimSpace(strings.ToLower(resp)) != "" && strings.TrimSpace(strings.ToLower(resp)) != "y" {
		conn.Write([]byte("DENIED\n"))
		fmt.Println("❌ 已拒绝")
		return
	}
	conn.Write([]byte("READY\n"))
	conn.SetReadBuffer(BufSz * 64)
	if v2 {
		recvV2(conn, fname, fsize, totalBlocks)
	} else {
		recvV1(conn, fname, fsize)
	}
}

func recvV1(conn *net.TCPConn, fname string, fsize int64) {
	if fname == "" {
		fname = "untitled"
	}
	f, err := os.Create(filepath.Join(cfg.SavePath, fname))
	if err != nil {
		return
	}
	defer f.Close()
	buf := make([]byte, BufSz)
	var recvd int64
	start := time.Now()
	last := start
	for recvd < fsize {
		n, _ := conn.Read(buf)
		if n == 0 {
			break
		}
		f.Write(buf[:n])
		recvd += int64(n)
		if time.Since(last) > 500*time.Millisecond {
			elapsed := time.Since(start).Seconds()
			fmt.Printf("\r📥 %.1f%% | %s / %s | %.1f MB/s", float64(recvd)*100/float64(fsize), fs(recvd), fs(fsize), float64(recvd)/elapsed/1024/1024)
			last = time.Now()
		}
	}
	fmt.Printf("\n✅ 完成! %s (%.1f MB/s)\n", fs(recvd), float64(recvd)/1024/1024/time.Since(start).Seconds())
	fmt.Print("> ")
}

func recvV2(conn *net.TCPConn, fname string, fsize int64, totalBlocks int) {
	if fname == "" {
		fname = "untitled"
	}
	path := filepath.Join(cfg.SavePath, fname)
	df, _ := os.Create(path)
	if fsize > 0 {
		df.Truncate(fsize)
	}
	df.Close()
	f, _ := os.OpenFile(path, os.O_RDWR, 0644)
	if f != nil {
		defer f.Close()
	}
	mp := make(map[int][]byte)
	done := 0
	r := bufio.NewReader(conn)
	start := time.Now()
	last := start
	recvd := int64(0)
	for done < totalBlocks {
		line, err := r.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "END|" {
			break
		}
		if !strings.HasPrefix(line, "BLOCK|") {
			continue
		}
		p := strings.Split(line, "|")
		if len(p) < 5 {
			continue
		}
		bi, _ := strconv.Atoi(p[1])
		bs, _ := strconv.Atoi(p[3])
		cs := p[4]
		data := make([]byte, bs)
		n, err := io.ReadFull(r, data)
		if err != nil || n != bs {
			break
		}
		recvd += int64(n)
		h := sha256.New()
		h.Write(data)
		if fmt.Sprintf("%x", h.Sum(nil)) != cs {
			continue
		}
		mp[bi] = data
		for {
			d, ok := mp[done]
			if !ok {
				break
			}
			if f != nil {
				f.WriteAt(d, int64(done)*ChunkSz)
			}
			done++
		}
		conn.Write([]byte(fmt.Sprintf("ACK|%d|%d\n", bi, n)))
		if time.Since(last) > 500*time.Millisecond {
			elapsed := time.Since(start).Seconds()
			fmt.Printf("\r📥 %.1f%% | %s / %s | %.1f MB/s [块 %d/%d]", float64(recvd)*100/float64(fsize), fs(recvd), fs(fsize), float64(recvd)/elapsed/1024/1024, bi+1, totalBlocks)
			last = time.Now()
		}
	}
	elapsed := time.Since(start).Seconds()
	fmt.Printf("\n✅ 完成! %s (%.1f MB/s)\n", fs(fsize), float64(fsize)/1024/1024/elapsed)
	fmt.Print("> ")
}

type ci struct {
	idx    int
	size   int
	cksum  string
	data   []byte
	acked  bool
}

func sendV2(conn *net.TCPConn, file *os.File, fname string, fsize int64) {
	blocks := int((fsize + ChunkSz - 1) / ChunkSz)
	workers := 1
	if cfg.EnableParallel {
		if blocks < 4 {
			workers = 2
		} else if blocks < 16 {
			workers = 4
		} else {
			workers = MaxWorkers
		}
	}
	h := sha256.New()
	file.Seek(0, 0)
	for buf := make([]byte, BufSz); ; {
		if n, _ := file.Read(buf); n == 0 {
			break
		} else {
			h.Write(buf[:n])
		}
	}
	fcs := fmt.Sprintf("%x", h.Sum(nil))
	fmt.Fprintf(bufio.NewWriter(conn), "%s\n%d\n%s\n%d\n%d\n%s\n", ProtoV2, len(fname), fname, fsize, blocks, fcs)
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	if resp, _ := bufio.NewReader(conn).ReadString('\n'); strings.Contains(resp, "DENIED") {
		fmt.Println("❌ 对方拒绝")
		return
	}
	conn.SetDeadline(time.Time{})
	conn.SetWriteBuffer(BufSz * 64)
	chunks := make([]ci, blocks)
	for i := 0; i < blocks; i++ {
		sz := ChunkSz
		off := int64(i) * ChunkSz
		if off+int64(sz) > fsize {
			sz = int(fsize - off)
		}
		chunks[i].idx, chunks[i].size = i, sz
		file.Seek(off, 0)
		chunks[i].data = make([]byte, sz)
		io.ReadFull(file, chunks[i].data)
		h2 := sha256.New()
		h2.Write(chunks[i].data)
		chunks[i].cksum = fmt.Sprintf("%x", h2.Sum(nil))
	}
	fmt.Printf("📤 %d workers, %d blocks\n", workers, blocks)
	var acked int32
	start := time.Now()
	go func() {
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(strings.TrimSpace(line), "ACK|") {
				if p := strings.Split(line, "|"); len(p) >= 2 {
					if idx, err := strconv.Atoi(p[1]); err == nil && !chunks[idx].acked {
						chunks[idx].acked = true
						atomic.AddInt32(&acked, 1)
					}
				}
			}
		}
	}()
	cq := make(chan int, blocks)
	for i := 0; i < blocks; i++ {
		cq <- i
	}
	close(cq)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range cq {
				c := &chunks[idx]
				conn.Write([]byte(fmt.Sprintf("BLOCK|%d|%d|%d|%s\n", c.idx, blocks, c.size, c.cksum)))
				conn.Write(c.data)
			}
		}()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	for acked < int32(blocks) {
		<-ticker.C
		elapsed := time.Since(start).Seconds()
		if elapsed > 0 {
			fmt.Printf("\r📤 %.1f%% | %d/%d | %.1f MB/s", float64(acked)*100/float64(blocks), acked, blocks, float64(acked)*float64(ChunkSz)/elapsed/1024/1024)
		}
	}
	ticker.Stop()
	conn.Write([]byte(fmt.Sprintf("END|%d|%s\n", blocks, fcs)))
	wg.Wait()
	fmt.Printf("\n✅ 完成! %s (%.1f MB/s)\n", fs(fsize), float64(fsize)/1024/1024/time.Since(start).Seconds())
}

func clientMode() {
	sendDiscoveryProbe()
	reader := bufio.NewReader(os.Stdin)
	for {
		listPeers()
		fmt.Print("> [s]扫描  [send]发送  [q]退出: ")
		inp, _ := reader.ReadString('\n')
		inp = strings.TrimSpace(strings.ToLower(inp))
		switch inp {
		case "q", "quit", "exit":
			fmt.Println("👋 再见!")
			stopCh <- true
			os.Exit(0)
		case "s", "scan":
			sendDiscoveryProbe()
			time.Sleep(500 * time.Millisecond)
			listPeers()
		default:
			if strings.HasPrefix(inp, "send ") || inp == "send" {
				parts := strings.Fields(inp)
				pid, fp := "", ""
				if len(parts) >= 3 {
					pid, fp = parts[1], strings.Join(parts[2:], " ")
				} else {
					listPeers()
					fmt.Print("设备ID: ")
					pid, _ = reader.ReadString('\n')
					pid = strings.TrimSpace(pid)
					fmt.Print("文件: ")
					fp, _ = reader.ReadString('\n')
					fp = strings.TrimSpace(fp)
				}
				if pid != "" && fp != "" {
					doSend(pid, fp)
				}
			}
		}
	}
}

func listPeers() {
	peersMut.RLock()
	defer peersMut.RUnlock()
	fmt.Println("\n📱 设备:")
	if len(peers) == 0 {
		fmt.Println("  (无)")
	} else {
		i := 1
		for _, p := range peers {
			fmt.Printf("  [%d] %s (%s:%d)\n", i, p.Name, p.IP, p.Port)
			i++
		}
	}
}

func sendDiscoveryProbe() {
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4bcast, Port: DiscPort})
	if err != nil {
		return
	}
	defer c.Close()
	ip := getOutboundIP()
	addrs := []string{"255.255.255.255:45678"}
	if p := strings.Split(ip, "."); len(p) == 4 {
		addrs = append(addrs, fmt.Sprintf("%s.%s.%s.255:45678", p[0], p[1], p[2]))
	}
	for _, a := range addrs {
		if udpAddr, err := net.ResolveUDPAddr("udp4", a); err == nil {
			c.WriteToUDP([]byte("LANDROP_DISCOVER"), udpAddr)
		}
	}
}

func doSend(pid, fp string) {
	peersMut.RLock()
	var peer *Peer
	if idx, err := strconv.Atoi(pid); err == nil && idx > 0 {
		i := 1
		for _, p := range peers {
			if i == idx {
				peer = p
				break
			}
			i++
		}
	} else if p, ok := peers[pid]; ok {
		peer = p
	}
	peersMut.RUnlock()
	if peer == nil {
		if parts := strings.Split(pid, ":"); len(parts) == 2 {
			var port int
			fmt.Sscanf(parts[1], "%d", &port)
			peer = &Peer{IP: parts[0], Port: port}
		}
	}
	if peer == nil {
		fmt.Println("❌ 设备无效")
		return
	}
	sendToPeer(peer, fp)
}

func sendToPeer(peer *Peer, fp string) {
	file, err := os.Open(fp)
	if err != nil {
		fmt.Printf("❌ 无法打开: %v\n", err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		fmt.Println("❌ 无效文件")
		return
	}
	fsize, fname := info.Size(), info.Name()
	conn, err := net.DialTCP("tcp4", nil, &net.TCPAddr{IP: net.ParseIP(peer.IP), Port: peer.Port})
	if err != nil {
		fmt.Printf("❌ 连接失败: %v\n", err)
		return
	}
	defer conn.Close()
	conn.SetKeepAlive(true)
	conn.SetKeepAlivePeriod(30 * time.Second)
	conn.SetWriteBuffer(BufSz * 64)
	fmt.Printf("📤 发送: %s (%s)\n", fname, fs(fsize))
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	w := bufio.NewWriter(conn)
	fmt.Fprintf(w, "%s\n%d\n%s\n%d\n", ProtoV2, len(fname), fname, fsize)
	w.Flush()
	resp, _ := bufio.NewReader(conn).ReadString('\n')
	if strings.Contains(resp, "DENIED") {
		fmt.Println("❌ 对方拒绝")
		return
	}
	conn.SetDeadline(time.Time{})
	if strings.TrimSpace(resp) == "READY" {
		fmt.Println("📤 使用 V2 协议")
		sendV2(conn, file, fname, fsize)
		return
	}
	fmt.Println("📤 使用 V1 协议")
	sendV1(conn, file, fname, fsize)
}

func sendV1(conn *net.TCPConn, file *os.File, fname string, fsize int64) {
	conn.SetWriteBuffer(BufSz * 64)
	w := bufio.NewWriter(conn)
	fmt.Fprintf(w, "%d\n%s\n%d\n", len(fname), fname, fsize)
	w.Flush()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	if resp, _ := bufio.NewReader(conn).ReadString('\n'); strings.Contains(resp, "DENIED") {
		fmt.Println("❌ 对方拒绝")
		return
	}
	conn.SetDeadline(time.Time{})
	start := time.Now()
	buf := make([]byte, BufSz)
	var sent int64
	for {
		n, err := file.Read(buf)
		if n == 0 {
			break
		}
		if err != nil && err.Error() != "EOF" {
			break
		}
		conn.Write(buf[:n])
		sent += int64(n)
		elapsed := time.Since(start).Seconds()
		if elapsed > 0 {
			fmt.Printf("\r📤 %.1f%% | %s / %s | %.1f MB/s", float64(sent)*100/float64(fsize), fs(sent), fs(fsize), float64(sent)/elapsed/1024/1024)
		}
	}
	fmt.Printf("\n✅ 完成! %s (%.1f MB/s)\n", fs(sent), float64(sent)/1024/1024/time.Since(start).Seconds())
}

func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func fs(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
