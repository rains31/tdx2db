package utils

import (
	"os"
)

// GetCacheDir 返回用于存放临时解压产物的缓存目录。
// 优先使用 /workspace/tmp，防止 Linux 下 /tmp (tmpfs 内存盘) 被撑满；
// 若 /workspace/tmp 不可写或不存在，则降级走系统的临时目录。
func GetCacheDir() (string, error) {
	parent := "/workspace/tmp"
	if fi, err := os.Stat(parent); err == nil && fi.IsDir() {
		// 尝试在 /workspace/tmp 创建临时子目录
		if appDir, err := os.MkdirTemp(parent, "tdx2db-temp-"); err == nil {
			return appDir, nil
		}
	}

	appDir, err := os.MkdirTemp("", "tdx2db-temp-")
	if err != nil {
		return "", err
	}

	return appDir, nil
}
