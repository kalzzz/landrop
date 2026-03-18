package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	DiscoveryPort = 45678
	TransferPort  = 45679
	BufferSize    = 64 * 1024
)

type Peer struct {
	ID       string
	Name     string
	IP       string
	Port     int
	LastSeen time.Time
}

type Config struct {
	DeviceName string
	SavePath   string
	IsServer   bool
}

var (
	config     Config
	peers      = make(map[string]*Peer)
	peersMutex sync.RWMutex
	stopChan   = make(chan bool)
)

func init() {
	hostname, _ := os.Hostname()
	flag.StringVar(&config.DeviceName, "name", hostname, "Device name")
	flag.StringVar(&config.SavePath, "path", "./downloads", "Save path for received files")
	flag.BoolVar(&config.IsServer, "server", false, "Run as receiver mode")
}

func main() {
	flag.Parse()

	if err := os.MkdirAll(config.SavePath, 0755); err != nil {
		log.Printf("Warning: Could not create download directory: %v", err)
		config.SavePath = os.TempDir()
	}

	log.SetFlags(0)
	log.SetPrefix("[" + config.DeviceName + "] ")

	fmt.Printf(`
╔═══════════════════════════════════════════╗
║          LANDrop - Go 实现               ║
║     局域网点对点文件传输工具             ║
║        支持大文件高速传输               ║
╚═══════════════════════════════════════════╝

设备名称: %s
保存路径: %s

`, config.DeviceName, config.SavePath)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n👋 再见!")
		stopChan <- true
		os.Exit(0)
	}()

	go runDiscoveryServer()

	if config.IsServer {
		runTransferServer()
	} else {
		runClientMode()
	}
}

func runDiscoveryServer() {
	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf(":%d", DiscoveryPort))
	if err != nil {
		log.Fatal(err)
	}

	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	log.Printf("🌐 服务发现已启动 (端口 %d)", DiscoveryPort)

	broadcastTicker := time.NewTicker(5 * time.Second)
	defer broadcastTicker.Stop()

	broadcastPresence(conn)

	buf := make([]byte, 1024)
	for {
		select {
		case <-stopChan:
			return
		case <-broadcastTicker.C:
			broadcastPresence(conn)
		default:
		}

		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				cleanupPeers()
				continue
			}
			continue
		}

		handleDiscoveryMessage(conn, remoteAddr, buf[:n])
	}
}

func broadcastPresence(conn *net.UDPConn) {
	ip := getOutboundIP()
	msg := fmt.Sprintf("LANDROP_RESPONSE|%s|%s|%d", config.DeviceName, ip, TransferPort)

	broadcastAddrs := []string{"255.255.255.255:45678"}
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		broadcastAddrs = append(broadcastAddrs, fmt.Sprintf("%s.%s.%s.255:45678", parts[0], parts[1], parts[2]))
	}

	for _, addrStr := range broadcastAddrs {
		if udpAddr, err := net.ResolveUDPAddr("udp4", addrStr); err == nil {
			conn.WriteToUDP([]byte(msg), udpAddr)
		}
	}
}

func handleDiscoveryMessage(conn *net.UDPConn, addr *net.UDPAddr, data []byte) {
	parts := strings.Split(string(data), "|")
	if len(parts) < 2 {
		return
	}

	switch parts[0] {
	case "LANDROP_DISCOVER":
		if config.IsServer {
			ip := getOutboundIP()
			msg := fmt.Sprintf("LANDROP_RESPONSE|%s|%s|%d", config.DeviceName, ip, TransferPort)
			conn.WriteToUDP([]byte(msg), addr)
		}

	case "LANDROP_RESPONSE":
		if len(parts) >= 4 {
			peerName := parts[1]
			peerIP := parts[2]
			peerPort := 0
			fmt.Sscanf(parts[3], "%d", &peerPort)

			if peerIP == getOutboundIP() {
				return
			}

			peerID := fmt.Sprintf("%s:%d", peerIP, peerPort)
			peersMutex.Lock()
			peers[peerID] = &Peer{
				ID:       peerID,
				Name:     peerName,
				IP:       peerIP,
				Port:     peerPort,
				LastSeen: time.Now(),
			}
			peersMutex.Unlock()
		}
	}
}

func cleanupPeers() {
	peersMutex.Lock()
	defer peersMutex.Unlock()

	timeout := 15 * time.Second
	now := time.Now()
	for id, peer := range peers {
		if now.Sub(peer.LastSeen) > timeout {
			delete(peers, id)
		}
	}
}

func runTransferServer() {
	addr, err := net.ResolveTCPAddr("tcp4", fmt.Sprintf(":%d", TransferPort))
	if err != nil {
		log.Fatal(err)
	}

	listener, err := net.ListenTCP("tcp4", addr)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	log.Printf("📁 文件传输服务已启动 (端口 %d)", TransferPort)
	log.Printf("💡 等待接收文件... (按 Ctrl+C 退出)")
	fmt.Println()

	for {
		select {
		case <-stopChan:
			return
		default:
		}

		listener.SetDeadline(time.Now().Add(1 * time.Second))
		conn, err := listener.AcceptTCP()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Printf("Accept error: %v", err)
			continue
		}

		go handleConnection(conn)
	}
}

func handleConnection(conn *net.TCPConn) {
	defer conn.SetDeadline(time.Now().Add(10 * time.Second))
	conn.SetDeadline(time.Now().Add(300 * time.Second))

	reader := bufio.NewReader(conn)

	var filename string
	var filesize int64

	var nameLen int
	fmt.Fscan(reader, &nameLen)
	if nameLen > 0 {
		nameBytes := make([]byte, nameLen)
		reader.Read(nameBytes)
		filename = string(nameBytes)
	}

	fmt.Fscan(reader, &filesize)

	fmt.Printf("\n📥 收到文件: %s (%s)\n", filename, formatSize(filesize))
	fmt.Print("> 是否接收? [Y/n]: ")

	reader2 := bufio.NewReader(os.Stdin)
	response, _ := reader2.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	if response != "" && response != "y" && response != "yes" {
		conn.Write([]byte("DENIED\n"))
		fmt.Println("❌ 已拒绝")
		return
	}

	conn.Write([]byte("READY\n"))

	savePath := filepath.Join(config.SavePath, filename)
	outFile, err := os.Create(savePath)
	if err != nil {
		log.Printf("创建文件失败: %v", err)
		return
	}
	defer outFile.Close()

	conn.SetReadBuffer(BufferSize * 64)

	buf := make([]byte, BufferSize)
	var received int64
	var lastPrint time.Time
	startTime := time.Now()

	for received < filesize {
		n, err := reader.Read(buf)
		if n == 0 {
			break
		}
		if err != nil && err.Error() != "EOF" {
			log.Printf("读取错误: %v", err)
			break
		}

		if _, werr := outFile.Write(buf[:n]); werr != nil {
			log.Printf("写入错误: %v", werr)
			break
		}
		received += int64(n)

		if time.Since(lastPrint) > 500*time.Millisecond {
			elapsed := time.Since(startTime).Seconds()
			speed := float64(received) / elapsed / 1024 / 1024
			percent := float64(received) * 100 / float64(filesize)
			fmt.Printf("\r📥 进度: %.1f%% | %s / %s | 速度: %.1f MB/s", 
				percent, formatSize(received), formatSize(filesize), speed)
			lastPrint = time.Now()
		}
	}

	elapsed := time.Since(startTime).Seconds()
	avgSpeed := float64(received) / 1024 / 1024 / elapsed
	fmt.Printf("\n✅ 传输完成! %s (平均 %.1f MB/s, 耗时 %.1fs)\n", 
		formatSize(received), avgSpeed, elapsed)
	fmt.Print("> ")
}

func runClientMode() {
	sendDiscoveryProbe()
	showMenu()
}

func sendDiscoveryProbe() {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   net.IPv4bcast,
		Port: DiscoveryPort,
	})
	if err != nil {
		log.Printf("发送探测失败: %v", err)
		return
	}
	defer conn.Close()

	ip := getOutboundIP()
	addrs := []string{"255.255.255.255:45678"}
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		addrs = append(addrs, fmt.Sprintf("%s.%s.%s.255:45678", parts[0], parts[1], parts[2]))
	}

	for _, addrStr := range addrs {
		if udpAddr, err := net.ResolveUDPAddr("udp4", addrStr); err == nil {
			conn.WriteToUDP([]byte("LANDROP_DISCOVER"), udpAddr)
		}
	}
}

func showMenu() {
	reader := bufio.NewReader(os.Stdin)

	for {
		printPeers()

		fmt.Print(`
操作:
  [s]      扫描设备
  [send]   发送文件
  [q]      退出

> `)

		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		input = strings.ToLower(input)

		if input == "q" || input == "quit" || input == "exit" {
			fmt.Println("👋 再见!")
			stopChan <- true
			os.Exit(0)
		}

		if input == "s" || input == "scan" {
			sendDiscoveryProbe()
			time.Sleep(500 * time.Millisecond)
			printPeers()
			continue
		}

		if input == "send" || strings.HasPrefix(input, "send ") {
			var peerID, filePath string
			parts := strings.Fields(input)
			
			if len(parts) >= 3 {
				peerID = parts[1]
				filePath = strings.Join(parts[2:], " ")
			} else {
				printPeers()
				fmt.Print("选择设备ID: ")
				peerID, _ = reader.ReadString('\n')
				peerID = strings.TrimSpace(peerID)
				fmt.Print("文件路径: ")
				filePath, _ = reader.ReadString('\n')
				filePath = strings.TrimSpace(filePath)
			}
			
			if peerID != "" && filePath != "" {
				sendFile(peerID, filePath)
			}
			continue
		}
	}
}

func printPeers() {
	peersMutex.RLock()
	defer peersMutex.RUnlock()

	fmt.Println("\n📱 发现以下设备:")
	if len(peers) == 0 {
		fmt.Println("  (暂无设备，请确保对方已开启接收模式)")
	} else {
		i := 1
		for _, peer := range peers {
			fmt.Printf("  [%d] %s (%s:%d)\n", i, peer.Name, peer.IP, peer.Port)
			i++
		}
	}
}

func sendFile(peerID, filePath string) {
	peersMutex.RLock()
	peer, exists := peers[peerID]
	peersMutex.RUnlock()

	if !exists {
		parts := strings.Split(peerID, ":")
		if len(parts) != 2 {
			fmt.Println("❌ 设备ID无效")
			return
		}
		port := TransferPort
		fmt.Sscanf(parts[1], "%d", &port)
		peer = &Peer{IP: parts[0], Port: port}
	}

	file, err := os.Open(filePath)
	if err != nil {
		fmt.Printf("❌ 无法打开文件: %v\n", err)
		return
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		fmt.Printf("❌ 无法获取文件信息: %v\n", err)
		return
	}

	if fileInfo.IsDir() {
		fmt.Println("❌ 不支持发送目录")
		return
	}

	filesize := fileInfo.Size()
	filename := fileInfo.Name()

	conn, err := net.DialTCP("tcp4", nil, &net.TCPAddr{
		IP:   net.ParseIP(peer.IP),
		Port: peer.Port,
	})
	if err != nil {
		fmt.Printf("❌ 连接失败: %v\n", err)
		return
	}
	defer conn.Close()

	conn.SetWriteBuffer(BufferSize * 64)

	fmt.Printf("📤 正在发送: %s (%s)\n", filename, formatSize(filesize))

	writer := bufio.NewWriter(conn)
	fmt.Fprintf(writer, "%d\n", len(filename))
	writer.WriteString(filename)
	fmt.Fprintf(writer, "\n%d\n", filesize)
	writer.Flush()

	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf("❌ 等待响应失败: %v\n", err)
		return
	}

	if strings.Contains(response, "DENIED") {
		fmt.Println("❌ 对方拒绝了文件传输")
		return
	}

	startTime := time.Now()
	sendBuf := make([]byte, BufferSize)
	var sent int64

	for {
		n, err := file.Read(sendBuf)
		if n == 0 {
			break
		}
		if err != nil && err.Error() != "EOF" {
			fmt.Printf("❌ 读取错误: %v\n", err)
			break
		}

		_, werr := conn.Write(sendBuf[:n])
		if werr != nil {
			fmt.Printf("❌ 发送错误: %v\n", werr)
			break
		}
		sent += int64(n)

		elapsed := time.Since(startTime).Seconds()
		if elapsed > 0 {
			speed := float64(sent) / elapsed / 1024 / 1024
			percent := float64(sent) * 100 / float64(filesize)
			fmt.Printf("\r📤 进度: %.1f%% | %s / %s | 速度: %.1f MB/s", 
				percent, formatSize(sent), formatSize(filesize), speed)
		}
	}

	elapsed := time.Since(startTime).Seconds()
	avgSpeed := float64(sent) / 1024 / 1024 / elapsed
	fmt.Printf("\n✅ 发送完成! %s (平均 %.1f MB/s, 耗时 %.1fs)\n", 
		formatSize(sent), avgSpeed, elapsed)
}

func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

func formatSize(bytes int64) string {
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
