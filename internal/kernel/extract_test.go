package kernel

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestExtractLifecycle(t *testing.T) {
	dir := t.TempDir()
	old, err := extract(dir, "kernel-v1")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(old)
	if err != nil {
		t.Fatal(err)
	}
	again, err := extract(dir, "kernel-v1")
	if err != nil || again != old {
		t.Fatalf("重复释放: %q %v", again, err)
	}
	againInfo, err := os.Stat(again)
	if err != nil || !os.SameFile(info, againInfo) {
		t.Fatal("未复用完整内核")
	}
	upgraded, err := extract(dir, "kernel-v2")
	if err != nil || upgraded == old {
		t.Fatalf("升级未选择新路径: %q %v", upgraded, err)
	}
	for _, change := range []string{"corrupt", "permission", "symlink"} {
		t.Run(change, func(t *testing.T) {
			switch change {
			case "corrupt":
				err = os.WriteFile(upgraded, []byte("corrupted"), 0o700)
			case "permission":
				err = os.Chmod(upgraded, 0o600)
			case "symlink":
				if err = os.Remove(upgraded); err == nil {
					err = os.Symlink(old, upgraded)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := extract(dir, "kernel-v2")
			if err != nil || got != upgraded {
				t.Fatalf("修复失败: %q %v", got, err)
			}
			data, err := os.ReadFile(got)
			if err != nil || string(data) != "kernel-v2" {
				t.Fatalf("内容不匹配: %q %v", data, err)
			}
			info, err := os.Lstat(got)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o700 {
				t.Fatalf("文件类型/权限错误: %v %v", info, err)
			}
		})
	}
	data, err := os.ReadFile(old)
	if err != nil || string(data) != "kernel-v1" {
		t.Fatal("旧内核被覆盖")
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, "kernels", ".sing-box-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("遗留临时文件: %v %v", leftovers, err)
	}
}

func TestExtractRejectsUnsafeDirectoryAndEmptyPayload(t *testing.T) {
	if _, err := extract(t.TempDir(), ""); err == nil {
		t.Fatal("接受了空内核")
	}
	for _, symlink := range []bool{false, true} {
		dir := t.TempDir()
		path := filepath.Join(dir, "kernels")
		var err error
		if symlink {
			err = os.Symlink(t.TempDir(), path)
		} else {
			err = os.WriteFile(path, []byte("not a directory"), 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := extract(dir, "kernel"); err == nil {
			t.Fatal("接受了不安全的内核目录")
		}
	}
}

func TestExtractConcurrent(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := extract(dir, "same-kernel"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	entries, err := os.ReadDir(filepath.Join(dir, "kernels"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("并发释放残留: %v %v", entries, err)
	}
}
