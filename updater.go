package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const repoAPI = "https://api.github.com/repos/Mojashi/claude-conversation-editor/releases/latest"

type GHRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []GHAsset `json:"assets"`
}

type GHAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type UpdateInfo struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	HasUpdate      bool   `json:"has_update"`
	DownloadURL    string `json:"download_url"`
}

func (a *App) GetVersion() string {
	return Version
}

func (a *App) CheckUpdate() (*UpdateInfo, error) {
	resp, err := http.Get(repoAPI)
	if err != nil {
		return nil, fmt.Errorf("failed to check updates: %w", err)
	}
	defer resp.Body.Close()

	var release GHRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse release: %w", err)
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	hasUpdate := latest != Version && latest != ""

	var downloadURL string
	if hasUpdate {
		for _, asset := range release.Assets {
			if strings.Contains(asset.Name, "darwin") && strings.HasSuffix(asset.Name, ".zip") {
				downloadURL = asset.BrowserDownloadURL
				break
			}
		}
	}

	return &UpdateInfo{
		CurrentVersion: Version,
		LatestVersion:  latest,
		HasUpdate:      hasUpdate,
		DownloadURL:    downloadURL,
	}, nil
}

func (a *App) DoUpdate(downloadURL string) error {
	// 自分のバイナリ（.appバンドル）のパスを取得
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// surgery.app/Contents/MacOS/surgery → surgery.app
	appBundle := exe
	for !strings.HasSuffix(appBundle, ".app") {
		parent := filepath.Dir(appBundle)
		if parent == appBundle {
			break
		}
		appBundle = parent
	}

	// zipをダウンロード
	tmpZip := os.TempDir() + "/surgery-update.zip"
	resp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	f, err := os.Create(tmpZip)
	if err != nil {
		return err
	}
	io.Copy(f, resp.Body)
	f.Close()

	// 展開してアプリを置き換え
	tmpDir := os.TempDir() + "/surgery-update"
	os.RemoveAll(tmpDir)
	os.MkdirAll(tmpDir, 0755)

	if err := unzip(tmpZip, tmpDir); err != nil {
		return fmt.Errorf("unzip failed: %w", err)
	}

	// 新しい .app を見つける
	newApp, err := filepath.Glob(tmpDir + "/*.app")
	if err != nil || len(newApp) == 0 {
		return fmt.Errorf("no .app found in update zip")
	}

	// 古いバンドルを削除して置き換え
	os.RemoveAll(appBundle)
	if err := exec.Command("cp", "-r", newApp[0], appBundle).Run(); err != nil {
		return fmt.Errorf("replace failed: %w", err)
	}

	// 再起動
	exec.Command("open", appBundle).Start()
	os.Exit(0)
	return nil
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		path := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(path, filepath.Clean(dest)+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(path, f.Mode())
			continue
		}
		os.MkdirAll(filepath.Dir(path), 0755)
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}
		rc, _ := f.Open()
		io.Copy(out, rc)
		rc.Close()
		out.Close()
	}
	return nil
}
