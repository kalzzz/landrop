# LANDrop (Go 实现)

一个简单的局域网点对点文件传输工具，支持大文件高速传输。

## 功能

- 🌐 自动发现局域网内的设备
- 📁 点对点文件传输
- ⚡ **支持大文件** (几GB到几十GB的音视频)
- 🔒 无需互联网，纯局域网传输
- 📱 跨平台 (Windows/Linux/macOS)

## 下载对应版本

| 平台 | 文件名 |
|------|--------|
| Windows x64 | `landrop-windows-amd64.exe` |
| Linux x64 | `landrop-linux-amd64` |
| Linux ARM64 | `landrop-linux-arm64` |
| macOS x64 | `landrop-darwin-amd64` |

## 使用方法

### 接收方 (先开启)

```bash
# Windows
landrop-windows-amd64.exe -server -name "我的电脑"

# Linux
./landrop-linux-amd64 -server -name "我的电脑"
```

### 发送方

```bash
# 扫描设备
./landrop-linux-amd64

# 交互界面:
#   s        - 扫描设备
#   send     - 发送文件
#   q        - 退出
```

## 传输大文件

```
📤 进度: 45.2% | 2.5 GB / 5.5 GB | 速度: 112.5 MB/s
✅ 传输完成! 5.5 GB (平均 98.3 MB/s, 耗时 57.3s)
```

### 传输速度说明

- 局域网速度通常受限于设备网卡 (100MB/s ~ 125MB/s)
- WiFi 5: 约 30-50 MB/s
- WiFi 6: 约 80-120 MB/s
- 有线千兆: 约 100+ MB/s

## 参数说明

| 参数 | 说明 |
|------|------|
| `--name` | 设备名称 |
| `--path` | 保存路径 (默认: ./downloads) |
| `--server` | 开启接收模式 |

## 协议

| 功能 | 端口 |
|------|------|
| 设备发现 | UDP 45678 |
| 文件传输 | TCP 45679 |

## 注意事项

1. 发送方和接收方需要在同一局域网
2. 防火墙需要放行 45678/45679 端口
3. 接收方需要先开启 `-server` 模式
4. 使用 WiFi 6 或有线连接可获得最佳速度
