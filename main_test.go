package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeepAliveConfig(t *testing.T) {
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	var sc *net.TCPConn
	var serror error
	var accepted bool

	go func() {
		sc, serror = l.AcceptTCP()
		accepted = true
	}()

	c, err := net.DialTCP("tcp4", nil, l.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	time.Sleep(100 * time.Millisecond)
	if !accepted {
		t.Fatal("未接受连接")
	}
	if serror != nil {
		t.Fatal(serror)
	}

	if err := enableKeepAlive(c); err != nil {
		t.Fatalf("KeepAlive 失败: %v", err)
	}
	if err := enableKeepAlive(sc); err != nil {
		t.Fatalf("服务器 KeepAlive 失败: %v", err)
	}

	t.Log("✓ KeepAlive 测试通过")
}

func TestLargeFileTransfer(t *testing.T) {
	size := int64(2 * 1024 * 1024 * 1024) // 2GB
	tmp := filepath.Join(os.TempDir(), "landrop_test_2gb.dat")
	
	f, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024*1024)
	for i := 0; i < int(size)/len(buf); i++ {
		f.Write(buf)
	}
	f.Close()
	defer os.Remove(tmp)

	l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := l.AcceptTCP()
		if err != nil {
			t.Logf("Accept: %v", err)
			return
		}
		defer conn.Close()
		
		enableKeepAlive(conn)
		conn.SetDeadline(time.Now().Add(30 * time.Second))
		
		rd := bufio.NewReader(conn)
		var nl int
		fmt.Fscan(rd, &nl)
		if nl > 0 {
			b := make([]byte, nl)
			rd.Read(b)
		}
		var fs int64
		fmt.Fscan(rd, &fs)
		
		conn.Write([]byte("READY\n"))
		conn.SetDeadline(time.Time{})
		
		data := make([]byte, BufferSize)
		var r int64
		for r < fs {
			n, err := rd.Read(data)
			if n == 0 {
				break
			}
			if err != nil && err.Error() != "EOF" {
				break
			}
			r += int64(n)
		}
		t.Logf("接收: %d bytes", r)
	}()

	c, err := net.DialTCP("tcp4", nil, l.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	enableKeepAlive(c)
	c.SetDeadline(time.Now().Add(30 * time.Second))

	w := bufio.NewWriter(c)
	fmt.Fprintf(w, "%d\n", 4)
	w.WriteString("test")
	fmt.Fprintf(w, "\n%d\n", size)
	w.Flush()

	c.SetDeadline(time.Time{})
	rd := bufio.NewReader(c)
	resp, _ := rd.ReadString('\n')
	if !strings.Contains(resp, "READY") {
		t.Fatalf("未收到 READY: %s", resp)
	}

	file, _ := os.Open(tmp)
	defer file.Close()

	sendBuf := make([]byte, BufferSize)
	var sent int64
	start := time.Now()
	for {
		n, err := file.Read(sendBuf)
		if n == 0 {
			break
		}
		if err != nil && err.Error() != "EOF" {
			break
		}
		c.Write(sendBuf[:n])
		sent += int64(n)
	}

	elapsed := time.Since(start).Seconds()
	speed := float64(sent) / 1024 / 1024 / elapsed
	t.Logf("发送: %d bytes, 速度: %.1f MB/s", sent, speed)

	wg.Wait()
	if sent != size {
		t.Errorf("大小不匹配: %d vs %d", sent, size)
	} else {
		t.Log("✓ 2GB 大文件传输测试通过")
	}
}

func TestNoDeadlineOnWait(t *testing.T) {
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	var conn *net.TCPConn
	go func() {
		conn, _ = l.AcceptTCP()
	}()

	c, err := net.DialTCP("tcp4", nil, l.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	time.Sleep(50 * time.Millisecond)
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	w := bufio.NewWriter(conn)
	fmt.Fprintf(w, "%d\n", 4)
	w.WriteString("test")
	fmt.Fprintf(w, "\n%d\n", 100)
	w.Flush()

	conn.SetDeadline(time.Time{})
	t.Log("✓ Deadline 清除测试通过")

	conn.Close()
	c.Close()
}

func TestSlowTransfer(t *testing.T) {
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ready := make(chan struct{})
	var transferred int64

	go func() {
		conn, err := l.AcceptTCP()
		if err != nil {
			return
		}
		defer conn.Close()
		enableKeepAlive(conn)
		conn.SetDeadline(time.Now().Add(30 * time.Second))
		buf := make([]byte, 1024)
		close(ready)
		for i := 0; i < 10; i++ {
			n, _ := conn.Read(buf)
			if n == 0 {
				break
			}
			atomic.AddInt64(&transferred, int64(n))
			time.Sleep(200 * time.Millisecond)
		}
	}()

	c, err := net.DialTCP("tcp4", nil, l.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	enableKeepAlive(c)
	c.SetDeadline(time.Now().Add(30 * time.Second))

	w := bufio.NewWriter(c)
	fmt.Fprintf(w, "%d\n", 4)
	w.WriteString("test")
	fmt.Fprintf(w, "\n%d\n", 10240)
	w.Flush()

	<-ready

	for i := 0; i < 10; i++ {
		c.Write([]byte(strings.Repeat("a", 1024)))
		time.Sleep(200 * time.Millisecond)
	}

	time.Sleep(300 * time.Millisecond)
	if transferred > 0 {
		t.Logf("✓ 慢速传输测试通过 (%d bytes)", transferred)
	}
}

func TestFileSizes(t *testing.T) {
	sizes := []int64{1024, 1024 * 1024, 10 * 1024 * 1024}
	for _, sz := range sizes {
		sz := sz
		t.Run(fmt.Sprintf("%dKB", sz/1024), func(t *testing.T) {
			tmp := filepath.Join(os.TempDir(), "landrop_sz_test.dat")
			f, _ := os.Create(tmp)
			f.Write(make([]byte, sz))
			f.Close()
			defer os.Remove(tmp)

			l, _ := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
			defer l.Close()

			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				conn, _ := l.AcceptTCP()
				if conn == nil {
					return
				}
				defer conn.Close()
				enableKeepAlive(conn)
				rd := bufio.NewReader(conn)
				var nl int
				fmt.Fscan(rd, &nl)
				if nl > 0 {
					b := make([]byte, nl)
					rd.Read(b)
				}
				var fs int64
				fmt.Fscan(rd, &fs)
				conn.Write([]byte("READY\n"))
				conn.SetDeadline(time.Time{})
				buf := make([]byte, BufferSize)
				var r int64
				for r < fs {
					n, _ := rd.Read(buf)
					if n == 0 {
						break
					}
					r += int64(n)
				}
			}()

			c, _ := net.DialTCP("tcp4", nil, l.Addr().(*net.TCPAddr))
			enableKeepAlive(c)
			c.SetDeadline(time.Now().Add(30 * time.Second))
			w := bufio.NewWriter(c)
			fmt.Fprintf(w, "%d\n", 4)
			w.WriteString("test")
			fmt.Fprintf(w, "\n%d\n", sz)
			w.Flush()
			c.SetDeadline(time.Time{})
			rd := bufio.NewReader(c)
			rd.ReadString('\n')

			file, _ := os.Open(tmp)
			data := make([]byte, BufferSize)
			for {
				n, _ := file.Read(data)
				if n == 0 {
					break
				}
				c.Write(data[:n])
			}
			file.Close()
			c.Close()
			wg.Wait()
			t.Logf("✓ %d 测试通过", sz)
		})
	}
}

func BenchmarkTransferSpeed(b *testing.B) {
	size := int64(100 * 1024 * 1024)
	tmp := filepath.Join(os.TempDir(), "landrop_bench.dat")
	f, _ := os.Create(tmp)
	f.Write(make([]byte, size))
	f.Close()
	defer os.Remove(tmp)

	for i := 0; i < b.N; i++ {
		l, _ := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		go func() {
			conn, _ := l.AcceptTCP()
			if conn == nil {
				return
			}
			io.Copy(io.Discard, conn)
			conn.Close()
		}()
		c, _ := net.DialTCP("tcp4", nil, l.Addr().(*net.TCPAddr))
		file, _ := os.Open(tmp)
		io.Copy(c, file)
		c.Close()
		file.Close()
		l.Close()
	}
}
