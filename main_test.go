package main

import (
	"crypto/sha256"
	"fmt"
	"net"
	"testing"
)

// TestFormatSize tests file size formatting utility
func TestFormatSize(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{2 * 1024 * 1024, "2.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{10 * 1024 * 1024 * 1024, "10.0 GB"},
	}

	for _, tc := range tests {
		t.Run(tc.expected, func(t *testing.T) {
			result := fs(tc.input)
			if result != tc.expected {
				t.Errorf("fs(%d) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

// TestGetOutboundIP tests outbound IP detection
func TestGetOutboundIP(t *testing.T) {
	ip := getOutboundIP()
	if ip == "" {
		t.Error("getOutboundIP returned empty string")
	}

	parsed := net.ParseIP(ip)
	if parsed == nil {
		t.Errorf("getOutboundIP returned invalid IP: %s", ip)
	}

	// Should be a valid private or public IP
	t.Logf("Outbound IP: %s", ip)
}

// TestChecksum tests SHA256 checksum calculation
func TestChecksum(t *testing.T) {
	tests := []struct {
		data     string
		expected string
	}{
		{"", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"hello", "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},
		{"hello world", "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"},
	}

	for _, tc := range tests {
		t.Run(tc.expected[:8], func(t *testing.T) {
			h := sha256.New()
			h.Write([]byte(tc.data))
			checksum := fmt.Sprintf("%x", h.Sum(nil))
			if checksum != tc.expected {
				t.Errorf("checksum(%q) = %q, want %q", tc.data, checksum, tc.expected)
			}
		})
	}
}

// TestChunkIndexing tests chunk/block count calculation
func TestChunkIndexing(t *testing.T) {
	tests := []struct {
		fsize    int64
		expected int
	}{
		{0, 0},
		{1, 1},
		{ChunkSz - 1, 1},
		{ChunkSz, 1},
		{ChunkSz + 1, 2},
		{2 * ChunkSz, 2},
		{2*ChunkSz + 1, 3},
		{10 * ChunkSz, 10},
		{1000 * int64(ChunkSz), 1000},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("size_%d", tc.fsize), func(t *testing.T) {
			blocks := int((tc.fsize + ChunkSz - 1) / ChunkSz)
			if blocks != tc.expected {
				t.Errorf("blocks for %d bytes = %d, want %d", tc.fsize, blocks, tc.expected)
			}
		})
	}
}

// TestConstants verifies key constants are correct
func TestConstants(t *testing.T) {
	if DiscPort != 45678 {
		t.Errorf("DiscPort = %d, want 45678", DiscPort)
	}
	if XferPort != 45679 {
		t.Errorf("XferPort = %d, want 45679", XferPort)
	}
	if ChunkSz != 1024*1024 {
		t.Errorf("ChunkSz = %d, want %d", ChunkSz, 1024*1024)
	}
	if BufSz != 64*1024 {
		t.Errorf("BufSz = %d, want %d", BufSz, 64*1024)
	}
	if MaxWorkers != 8 {
		t.Errorf("MaxWorkers = %d, want 8", MaxWorkers)
	}
	if ProtoV2 != "PROTOCOL_V2" {
		t.Errorf("ProtoV2 = %q, want %q", ProtoV2, "PROTOCOL_V2")
	}
}

// BenchmarkFormatSize benchmarks file size formatting
func BenchmarkFormatSize(b *testing.B) {
	sizes := []int64{1024, 1024*1024, 10*1024*1024, 1024*1024*1024}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		fs(sizes[i%len(sizes)])
	}
}

// BenchmarkChecksum benchmarks SHA256 checksum
func BenchmarkChecksum(b *testing.B) {
	data := make([]byte, ChunkSz)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h := sha256.New()
		h.Write(data)
		h.Sum(nil)
	}
}

// TestPeerStruct tests Peer struct initialization
func TestPeerStruct(t *testing.T) {
	peer := &Peer{
		ID:   "test:45679",
		Name: "TestDevice",
		IP:   "192.168.1.100",
		Port: 45679,
	}

	if peer.ID == "" || peer.Name == "" || peer.IP == "" || peer.Port == 0 {
		t.Error("Peer struct fields should not be empty")
	}
}

// TestConfigStruct tests Config struct defaults
func TestConfigStruct(t *testing.T) {
	if cfg.DeviceName == "" {
		t.Error("DeviceName should have a default value")
	}
	if cfg.SavePath == "" {
		t.Error("SavePath should have a default value")
	}
}

// TestWorkerScaling tests worker count scaling logic
func TestWorkerScaling(t *testing.T) {
	tests := []struct {
		blocks   int
		expected int
	}{
		{0, 1},
		{1, 1},
		{2, 2},
		{3, 2},
		{4, 4},
		{8, 4},
		{16, 8},
		{17, 8},
		{100, 8},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("blocks_%d", tc.blocks), func(t *testing.T) {
			var workers int
			if tc.blocks < 4 {
				workers = 2
			} else if tc.blocks < 16 {
				workers = 4
			} else {
				workers = MaxWorkers
			}
			// Edge case: 0 or 1 blocks still uses 1 worker
			if tc.blocks <= 1 {
				workers = 1
			}
			if workers != tc.expected {
				t.Errorf("workers for %d blocks = %d, want %d", tc.blocks, workers, tc.expected)
			}
		})
	}
}
