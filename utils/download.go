package utils

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// 创建一个带超时的自定义客户端
var downloadClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 10 * time.Second,
	},
}

// TimeoutReader 监控读取进度，如果长时间没数据则返回错误
type TimeoutReader struct {
	io.ReadCloser
	timeout time.Duration
	cancel  context.CancelFunc
}

func (r *TimeoutReader) Read(p []byte) (n int, err error) {
	// 每次读取都重置计时器（由于 context 本身不支持动态重置，我们简单通过循环实现）
	// 但更优雅的方式是结合 context.WithTimeout 在外部控制
	return r.ReadCloser.Read(p)
}

// DownloadFileWithResume 下载文件并支持断点续传和自动重试
func DownloadFileWithResume(url string, destPath string) error {
	maxRetries := 10
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			fmt.Printf("🔄 发现卡顿或断线，正在进行第 %d 次重试... (等待 3 秒)\n", i)
			time.Sleep(3 * time.Second)
		}
		
		err := downloadAttempt(url, destPath)
		if err == nil {
			return nil
		}
		
		// 404 错误直接熔断，不重试
		if err.Error() == "HTTP 404" {
			return err
		}
		
		lastErr = err
		fmt.Printf("⚠️  下载中断: %v\n", err)
	}

	return fmt.Errorf("在 %d 次重试后仍然失败: %w", maxRetries, lastErr)
}

func downloadAttempt(url string, destPath string) error {
	partPath := destPath + ".part"
	var startBytes int64 = 0

	if stat, err := os.Stat(partPath); err == nil {
		startBytes = stat.Size()
	}

	// 使用带超时的 Context 控制整个请求生命周期中的“活跃度”
	// 注意：不能简单用 Timeout，因为大文件下载时间长。
	// 我们通过自定义 Transport 或在 Copy 过程中手动干预
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	if startBytes > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startBytes))
	}

	resp, err := downloadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	if startBytes > 0 && resp.StatusCode == http.StatusOK {
		startBytes = 0
	}

	out, err := os.OpenFile(partPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	// 核心改进：启动一个守护协程，监控下载进度
	done := make(chan struct{})
	lastPos := startBytes
	currentPos := startBytes
	
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if currentPos <= lastPos {
					fmt.Println("🛑 检测到传输僵死（10秒无流量），正在强制重连...")
					cancel() // 掐断 HTTP 请求
					return
				}
				lastPos = currentPos
			}
		}
	}()

	// 包装一下 Writer 来追踪进度
	progressWriter := &progressTrackingWriter{
		Writer: out,
		OnWrite: func(n int) {
			currentPos += int64(n)
		},
	}

	_, err = io.Copy(progressWriter, resp.Body)
	close(done) // 通知监控协程退出

	if err != nil {
		return fmt.Errorf("传输失败: %w", err)
	}

	return os.Rename(partPath, destPath)
}

type progressTrackingWriter struct {
	io.Writer
	OnWrite func(int)
}

func (w *progressTrackingWriter) Write(p []byte) (n int, err error) {
	n, err = w.Writer.Write(p)
	if n > 0 {
		w.OnWrite(n)
	}
	return n, err
}
