package tdx

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

//go:embed  embed/*
var embedFS embed.FS
var startDateStr = "19901201"

// DatatoolCreate merges TDX incremental data into per-stock files.
func DatatoolCreate(cacheDir, vipdocDir, subCommand string, endDate time.Time) error {
	switch subCommand {
	case "day", "min", "tick":
	default:
		return errors.New("unsupported datatool subcommand: " + subCommand)
	}

	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if subCommand == "day" {
			return NativeDayMerge(filepath.Join(cacheDir, "vipdoc"))
		}
		fmt.Printf("⚠️  %s 子命令暂不支持 %s，已跳过\n", subCommand, runtime.GOOS)
		return nil
	}

	return datatoolExec(cacheDir, vipdocDir, subCommand, endDate)
}

func datatoolExec(cacheDir, vipdocDir, subCommand string, endDate time.Time) error {
	toolPath, err := extractDatatool(cacheDir)
	if err != nil {
		return fmt.Errorf("failed to extract datatool: %w", err)
	}

	endDateStr := endDate.Format("20060102")

	// 核心修复：确保 vipdocDir 是绝对路径，并强制建立链接
	absVipdocDir, err := filepath.Abs(vipdocDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for vipdocDir: %w", err)
	}

	vipdocInCache := filepath.Join(cacheDir, "vipdoc")
	// 清理旧的链接或目录
	_ = os.Remove(vipdocInCache)
	
	if err := os.Symlink(absVipdocDir, vipdocInCache); err != nil {
		return fmt.Errorf("failed to create symlink for vipdoc: %w", err)
	}

	cmd := exec.Command(toolPath, subCommand, "create", startDateStr, endDateStr)
	cmd.Dir = cacheDir
	
	// 1分钟数据转换量巨大，我们选择静默 Stdout，仅在 Stdout 有输出时打印一个点证明程序活着
	fmt.Printf("🚀 正在转档 %s 数据... ", subCommand)
	
	// 丢弃标准输出，但保留错误输出（转码为 UTF-8）
	utf8ErrWriter := transform.NewWriter(os.Stderr, simplifiedchinese.GBK.NewDecoder())
	cmd.Stdout = nil // 静默处理
	cmd.Stderr = utf8ErrWriter
	
	if err := cmd.Run(); err != nil {
		fmt.Println(" ❌")
		return fmt.Errorf("failed to execute datatool command: %w", err)
	}

	fmt.Println(" ✅")
	return nil
}

func extractDatatool(cacheDir string) (string, error) {
	toolPath, err := extractFileFromEmbed(cacheDir, "embed/datatool")
	if err != nil {
		return "", fmt.Errorf("failed to extract binary: %w", err)
	}

	if _, err := extractFileFromEmbed(cacheDir, "embed/datatool.ini"); err != nil {
		return "", fmt.Errorf("failed to extract config: %w", err)
	}

	return toolPath, nil
}

func extractFileFromEmbed(cacheDir string, srcPath string) (string, error) {
	destFileName := filepath.Base(srcPath)
	destPath := filepath.Join(cacheDir, destFileName)

	data, err := embedFS.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("failed to read embedded file %s: %w", srcPath, err)
	}

	if err := os.WriteFile(destPath, data, 0755); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", destPath, err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(destPath, 0755); err != nil {
			return "", fmt.Errorf("failed to set file permissions for %s: %w", destPath, err)
		}
	}
	return destPath, nil
}
