// Package updater provides self-update capabilities for the InfraLens agent.
package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Version is the current agent version (set at build time)
var Version = "2.1.0"

// releaseBaseURL is the only origin the agent will install a binary from.
// It is a constant on purpose: the download location must not be derivable
// from anything the backend says.
const releaseBaseURL = "https://github.com/Herenn/Infralens/releases/download"

// semverPattern constrains the version string before it is interpolated into
// a download URL, so a hostile value cannot redirect the request elsewhere.
var semverPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)

// VersionInfo represents version information from the backend.
//
// Note there is deliberately no UpdateURL field. The agent used to take a
// download location straight from this response and install whatever it
// found there, which handed anyone able to answer the version request — a
// hostile backend, or a network attacker, since the backend may be plain
// HTTP — arbitrary code execution as root on every node.
type VersionInfo struct {
	Version     string `json:"version"`
	CommitHash  string `json:"commit_hash,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
}

// Updater handles agent self-updates
type Updater struct {
	backendURL    string
	apiKey        string
	checkInterval time.Duration
	httpClient    *http.Client
	onUpdate      func() // Callback when update is available
}

// NewUpdater creates a new updater instance.
// backendURL may be a full URL ("https://host") or a bare host:port.
// apiKey is sent with version checks; without it a backend that has
// authentication enabled rejects them and updates never happen.
func NewUpdater(backendURL, apiKey string, checkInterval time.Duration) *Updater {
	backendURL = strings.TrimRight(strings.TrimSpace(backendURL), "/")
	if backendURL != "" && !strings.HasPrefix(backendURL, "http://") && !strings.HasPrefix(backendURL, "https://") {
		backendURL = "http://" + backendURL
	}
	return &Updater{
		backendURL:    backendURL,
		apiKey:        apiKey,
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
	url := fmt.Sprintf("%s/api/v1/version", u.backendURL)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, false, fmt.Errorf("failed to build version request: %w", err)
	}
	if u.apiKey != "" {
		req.Header.Set("X-API-Key", u.apiKey)
	}

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("failed to check version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, false, fmt.Errorf(
			"version check rejected (status %d): check --api-key matches the backend API_KEY",
			resp.StatusCode)
	}

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

	// The version string is interpolated into a URL path, so it has to look
	// like a version and nothing else.
	if !semverPattern.MatchString(info.Version) {
		return fmt.Errorf("refusing to update: backend reported a malformed version %q", info.Version)
	}

	// Always built from a pinned HTTPS origin. Never from anything the
	// backend supplied.
	assetName := fmt.Sprintf("infralens-agent-linux-%s", runtime.GOARCH)
	downloadURL := fmt.Sprintf("%s/%s/%s", releaseBaseURL, info.Version, assetName)

	log.WithField("url", downloadURL).Info("Downloading new version...")

	// Fetch the published digest first: if it is unavailable we have no way
	// to tell a genuine release binary from anything else, so we stop.
	wantSum, err := u.fetchChecksum(downloadURL + ".sha256")
	if err != nil {
		return fmt.Errorf("refusing to install unverified update: %w", err)
	}

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

	// Write to temporary file, hashing as we go
	tmpPath := execPath + ".new"
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	hasher := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmpFile, hasher), resp.Body)
	tmpFile.Close()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write update: %w", err)
	}

	gotSum := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(gotSum, wantSum) {
		os.Remove(tmpPath)
		return fmt.Errorf("checksum mismatch: expected %s, got %s - update discarded", wantSum, gotSum)
	}
	log.WithField("sha256", gotSum).Info("Update checksum verified")

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

// fetchChecksum retrieves the published sha256 digest for a release asset.
// The digest file contains the hex digest alone.
func (u *Updater) fetchChecksum(url string) (string, error) {
	resp, err := u.httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch checksum: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum fetch returned status %d", resp.StatusCode)
	}

	// Bounded read: a digest is 64 hex characters.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", fmt.Errorf("failed to read checksum: %w", err)
	}

	sum := strings.TrimSpace(string(data))
	if i := strings.IndexAny(sum, " \t"); i > 0 {
		sum = sum[:i] // tolerate "<digest>  <filename>" form
	}
	if len(sum) != hex.EncodedLen(sha256.Size) {
		return "", fmt.Errorf("malformed checksum %q", sum)
	}
	if _, err := hex.DecodeString(sum); err != nil {
		return "", fmt.Errorf("malformed checksum %q", sum)
	}
	return sum, nil
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
