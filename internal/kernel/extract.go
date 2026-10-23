package kernel

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func extract(configDir, content string) (string, error) {
	if content == "" {
		return "", fmt.Errorf("内置 sing-box 为空")
	}
	dir, err := filepath.Abs(filepath.Join(configDir, "kernels"))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建内核目录: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("内核目录不是普通目录: %s", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = io.Copy(hash, strings.NewReader(content))
	digest := fmt.Sprintf("%x", hash.Sum(nil))
	// 内容寻址使升级不覆盖正在运行的内核，也不会选中历史版本。
	target := filepath.Join(dir, "sing-box-"+digest)
	if validFile(target, digest, int64(len(content))) {
		return target, nil
	}
	tmp, err := os.CreateTemp(dir, ".sing-box-*")
	if err != nil {
		return "", fmt.Errorf("释放内核: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer tmp.Close()
	if _, err := io.Copy(tmp, strings.NewReader(content)); err != nil {
		return "", err
	}
	if err := tmp.Chmod(0o700); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if !validFile(tmp.Name(), digest, int64(len(content))) {
		return "", fmt.Errorf("释放的 sing-box 完整性校验失败")
	}
	// 同目录原子替换；损坏文件和符号链接都不就地写入。
	if err := os.Rename(tmp.Name(), target); err != nil {
		return "", fmt.Errorf("安装内核: %w", err)
	}
	return target, nil
}

func validFile(path, digest string, size int64) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size || info.Mode().Perm() != 0o700 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return false
	}
	return fmt.Sprintf("%x", hash.Sum(nil)) == digest
}
