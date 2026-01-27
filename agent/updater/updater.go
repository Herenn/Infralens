// Package updater provides self-update capabilities for the InfraLens agent.
package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	log "github.com/sirupsen/logrus"
)

// Version is the current agent version (set at build time)
var Version = "0.2.2"

// VersionInfo represents version information from the backend
type VersionInfo struct {
	Version     string `json:"version"`
	CommitHash  string `json:"commit_hash,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
	UpdateURL   string `json:"update_url,omitempty"`
}

// Updater handles agent self-updates
type Updater struct {
	backendAddr   string
	checkInterval time.Duration
	httpClient    *http.Client
	onUpdate      func() // Callback when update is available
}

// NewUpdater creates a new updater instance
func NewUpdater(backendAddr string, checkInterval time.Duration) *Updater {
	return &Updater{
		backendAddr:   backendAddr,
		checkInterval: checkInterval,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

// SetUpdateCallback sets a callback function to be called when an update is available
func (u *Updater) SetUpdateCallback(callback func()) {
	u.onUpdate = callback
}

// CheckVersion checks if a newer version is available
func (u *Updater) CheckVersion() (*VersionInfo, bool, error) {
	url := fmt.Sprintf("http://%s/api/v1/version", u.backendAddr)
	resp, err := u.httpClient.Get(url)
	if err != nil {
		return nil, false, fmt.Errorf("failed to check version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("version check returned status %d", resp.StatusCode)
	}

	var info VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, false, fmt.Errorf("failed to decode version info: %w", err)
	}

	// Compare versions
	needsUpdate := info.Version != "" && info.Version != Version && info.Version != "dev"

	return &info, needsUpdate, nil
}

// StartPeriodicCheck starts periodic version checking
func (u *Updater) StartPeriodicCheck(stopCh <-chan struct{}) {
	ticker := time.NewTicker(u.checkInterval)
	defer ticker.Stop()

	// Initial check after a short delay
	time.Sleep(10 * time.Second)
	u.doCheck()

	for {
		select {
		case <-ticker.C:
			u.doCheck()
		case <-stopCh:
			log.Info("Version checker stopped")
			return
		}
	}
}

func (u *Updater) doCheck() {
	info, needsUpdate, err := u.CheckVersion()
	if err != nil {
		log.WithError(err).Debug("Version check failed")
		return
	}

	if needsUpdate {
		log.WithFields(log.Fields{
			"current": Version,
			"latest":  info.Version,
		}).Info("New version available!")

		if u.onUpdate != nil {
			u.onUpdate()
		}
	}
}

// SelfUpdate downloads and installs the latest version
func (u *Updater) SelfUpdate() error {
	info, needsUpdate, err := u.CheckVersion()
	if err != nil {
		return err
	}

	if !needsUpdate {
		log.Info("Already running the latest version")
		return nil
	}

	// Determine download URL
	downloadURL := info.UpdateURL
	if downloadURL == "" {
		// Construct GitHub release URL
		arch := runtime.GOARCH
		downloadURL = fmt.Sprintf(
			"https://github.com/Herenn/Infralens/releases/download/%s/infralens-agent-linux-%s",
			info.Version, arch,
		)
	}

	log.WithField("url", downloadURL).Info("Downloading new version...")

	// Download new binary
	resp, err := u.httpClient.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	// Get current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Write to temporary file
	tmpPath := execPath + ".new"
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	_, err = io.Copy(tmpFile, resp.Body)
	tmpFile.Close()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write update: %w", err)
	}

	// Backup old binary
	backupPath := execPath + ".old"
	if err := os.Rename(execPath, backupPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to backup old binary: %w", err)
	}

	// Move new binary into place
	if err := os.Rename(tmpPath, execPath); err != nil {
		// Try to restore backup
		os.Rename(backupPath, execPath)
		return fmt.Errorf("failed to install update: %w", err)
	}

	// Remove backup
	os.Remove(backupPath)

	log.WithField("version", info.Version).Info("Update installed successfully!")
	return nil
}

// RestartSelf restarts the agent process using systemctl
func RestartSelf() error {
	log.Info("Restarting agent via systemctl...")

	cmd := exec.Command("systemctl", "restart", "infralens-agent")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restart failed: %v - %s", err, string(output))
	}

	return nil
}

// GetVersion returns the current version
func GetVersion() string {
	return Version
}
