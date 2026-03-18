# LANDrop (Go 实现)

一个简单的局域网点对点文件传输工具，支持大文件高速传输。

## 功能

- 🌐 自动发现局域网内的设备
- 📁 点对点文件传输
- ⚡ **支持大文件** (几GB到几十GB的音视频)
- 🔒 无需互联网，纯局域网传输
- 📱 跨平台 (Windows/Linux/macOS)

## 下载

**[Release 页面](https://github.com/kalzzz/landrop/releases)** - 下载对应平台的二进制文件

| 平台 | 文件名 |
|------|--------|
| Windows x64 | `landrop-windows-amd64.exe` |
| Linux x64 | `landrop-linux-amd64` |
| Linux ARM64 | `landrop-linux-arm64` |
| macOS x64 | `landrop-darwin-amd64` |

## 快速开始

### 1. 接收方 (先开启)

```bash
# Windows
landrop-windows-amd64.exe -server -name "我的电脑"

# Linux / macOS
./landrop-linux-amd64 -server -name "我的电脑"
```

### 2. 发送方

```bash
./landrop -name "发送端"
```

### 3. 交互界面

```
📱 发现以下设备:
  [1] 我的电脑 (192.168.1.100:45679)

操作:
  [s]      扫描设备
  [send]   发送文件
  [q]      退出

> send 1 test.mp4    # 发送文件给第1个设备
# 或
> send 192.168.1.100:45679 test.mp4  # 直接指定 IP:端口
```

## 使用说明

### 发送文件

方式一：使用数字序号
```
> send 1 filename.ext
```

方式二：使用 IP:端口
```
> send 192.168.1.100:45679 filename.ext
```

### 命令

| 命令 | 说明 |
|------|------|
| `s` 或 `scan` | 扫描局域网设备 |
| `send <序号/IP> <文件>` | 发送文件 |
| `q` 或 `quit` | 退出 |

### 参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `--name` | 设备名称 | 主机名 |
| `--path` | 保存路径 | `./downloads` |
| `--server` | 开启接收模式 | 否 |

## 传输大文件

```
📥 收到文件: vacation.mov (2.5 GB)
> 是否接收? [Y/n]: y
📥 进度: 45.2% | 1.1 GB / 2.5 GB | 速度: 112.5 MB/s
✅ 传输完成! 2.5 GB (平均 98.3 MB/s, 耗时 25.7s)
```

### 速度说明

- 局域网速度受限于设备网卡
- WiFi 5: 约 30-50 MB/s
- WiFi 6: 约 80-120 MB/s
- 有线千兆: 约 100+ MB/s

## 协议

| 功能 | 端口 | 协议 |
|------|------|------|
| 设备发现 | 45678 | UDP |
| 文件传输 | 45679 | TCP |

## 常见问题

**Q: 收不到设备？**
A: 确保接收方已开启 `-server` 模式，双方在同一局域网

**Q: 传输中断？**
A: 已修复大文件传输超时问题，使用 KeepAlive 保活连接

**Q: 文件名乱码？**
A: 已修复文件名解析，支持空格和特殊字符

## 开发

```bash
# 编译
go build -o landrop

# 测试
go test -v

# 交叉编译
GOOS=linux GOARCH=amd64 go build -o landrop-linux-amd64
GOOS=windows GOARCH=amd64 go build -o landrop-windows-amd64.exe
GOOS=darwin GOARCH=amd64 go build -o landrop-darwin-amd64
```
