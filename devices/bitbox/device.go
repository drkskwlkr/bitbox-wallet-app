// Package bitbox contains the API to the physical device.
package bitbox

import (
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcutil/hdkeychain"
	"github.com/shiftdevices/godbb/util/errp"
	"github.com/shiftdevices/godbb/util/jsonp"
	"github.com/shiftdevices/godbb/util/semver"
	"golang.org/x/crypto/pbkdf2"
)

var (
	lowestSupportedFirmwareVersion    = semver.NewSemVer(2, 2, 3)
	lowestNonSupportedFirmwareVersion = semver.NewSemVer(3, 0, 0)
)

// Event instances are sent to the onEvent callback.
type Event string

const (
	// EventStatusChanged is fired when the status changes. Check the status using Status().
	EventStatusChanged Event = "statusChanged"
)

// CommunicationInterface contains functions needed to communicate with the device.
//go:generate mockery -name CommunicationInterface
type CommunicationInterface interface {
	SendPlain(string) (map[string]interface{}, error)
	SendEncrypt(string, string) (map[string]interface{}, error)
	Close()
}

// Interface is the API of a Device
type Interface interface {
	Status() Status
	DeviceInfo() (*DeviceInfo, error)
	SetPassword(string) error
	CreateWallet(string) error
	Login(string) (bool, string, error)
	Reset() (bool, error)
	EraseBackup(string) error
	RestoreBackup(string, string) (bool, error)
	CreateBackup(string) error
	BackupList() ([]string, error)
}

// DeviceInfo is the data returned from the device info api call.
type DeviceInfo struct {
	Version   string `json:"version"`
	Serial    string `json:"serial"`
	ID        string `json:"id"`
	TFA       string `json:"TFA"`
	Bootlock  bool   `json:"bootlock"`
	Name      string `json:"name"`
	SDCard    bool   `json:"sdcard"`
	Lock      bool   `json:"lock"`
	U2F       bool   `json:"U2F"`
	U2FHijack bool   `json:"U2F_hijack"`
	Seeded    bool   `json:"seeded"`
}

// Device provides the API to communicate with the digital bitbox.
type Device struct {
	deviceID      string
	communication CommunicationInterface
	onEvent       func(Event)

	// If set, the device  is configured with a password.
	initialized bool

	// If set, the user is "logged in".
	password string

	// If set, the device contains a wallet.
	seeded bool

	closed bool
}

// NewDevice creates a new instance of Device.
// communication is used for transporting messages to/from the device.
func NewDevice(deviceID string, firmwareVersion *semver.SemVer, communication CommunicationInterface) (*Device, error) {
	if !firmwareVersion.Between(lowestSupportedFirmwareVersion, lowestNonSupportedFirmwareVersion) {
		return nil, errp.Newf("The firmware version '%s' is not supported.", firmwareVersion)
	}

	device := &Device{
		deviceID:      deviceID,
		communication: communication,
		onEvent:       nil,

		closed: false,
	}

	// Sleep a bit to wait for the device to initialize. Sending commands too early means the
	// internal memory might not be initialized, and we run into the password retry check, requiring
	// a long touch by the user.  TODO: fix in the firmware, then remove this sleep.
	time.Sleep(1 * time.Second)

	// Ping to check if the device is initialized. Sometimes, booting takes a couple of seconds, so
	// repeat the command until it is ready.
	var initialized bool
	for i := 0; i < 20; i++ {
		var err error
		initialized, err = device.Ping()
		if err != nil {
			if dbbErr, ok := err.(*Error); ok && dbbErr.Code == ErrInitializing {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			return nil, err
		}
		break
	}
	device.initialized = initialized
	return device, nil
}

// DeviceID returns the device ID (provided when it was created in the constructor).
func (dbb *Device) DeviceID() string {
	return dbb.deviceID
}

// SetOnEvent installs a callback which is called for various events.
func (dbb *Device) SetOnEvent(onEvent func(Event)) {
	dbb.onEvent = onEvent
}

func (dbb *Device) onStatusChanged() {
	if dbb.onEvent != nil {
		dbb.onEvent(EventStatusChanged)
	}
}

// Status returns the device state. See the Status* constants.
func (dbb *Device) Status() Status {
	if dbb.seeded {
		return StatusSeeded
	}
	if dbb.password != "" {
		return StatusLoggedIn
	}
	if dbb.initialized {
		return StatusInitialized
	}
	return StatusUninitialized
}

// Close closes the HID device.
func (dbb *Device) Close() {
	dbb.communication.Close()
	dbb.closed = true
}

func (dbb *Device) sendPlain(key, val string) (map[string]interface{}, error) {
	jsonText, err := json.Marshal(map[string]string{key: val})
	if err != nil {
		return nil, err
	}
	return dbb.communication.SendPlain(string(jsonText))
}

func (dbb *Device) send(value interface{}, password string) (map[string]interface{}, error) {
	return dbb.communication.SendEncrypt(string(jsonp.MustMarshal(value)), password)
}

func (dbb *Device) sendKV(key, value, password string) (map[string]interface{}, error) {
	return dbb.send(map[string]string{key: value}, password)
}

func (dbb *Device) deviceInfo(password string) (*DeviceInfo, error) {
	reply, err := dbb.sendKV("device", "info", password)
	if err != nil {
		return nil, err
	}
	deviceInfo := &DeviceInfo{}

	device, ok := reply["device"].(map[string]interface{})
	if !ok {
		return nil, errp.New("unexpected reply")
	}
	if deviceInfo.Serial, ok = device["serial"].(string); !ok {
		return nil, errp.New("no serial")
	}
	if deviceInfo.ID, ok = device["id"].(string); !ok {
		return nil, errp.New("no id")
	}
	if deviceInfo.TFA, ok = device["TFA"].(string); !ok {
		return nil, errp.New("no TFA")
	}
	if deviceInfo.Bootlock, ok = device["bootlock"].(bool); !ok {
		return nil, errp.New("no bootlock")
	}
	if deviceInfo.Name, ok = device["name"].(string); !ok {
		return nil, errp.New("device name")
	}
	if deviceInfo.SDCard, ok = device["sdcard"].(bool); !ok {
		return nil, errp.New("SD card")
	}
	if deviceInfo.Lock, ok = device["lock"].(bool); !ok {
		return nil, errp.New("lock")
	}
	if deviceInfo.U2F, ok = device["U2F"].(bool); !ok {
		return nil, errp.New("U2F")
	}
	if deviceInfo.U2FHijack, ok = device["U2F_hijack"].(bool); !ok {
		return nil, errp.New("U2F_hijack")
	}
	if deviceInfo.Version, ok = device["version"].(string); !ok {
		return nil, errp.New("version")
	}
	if deviceInfo.Seeded, ok = device["seeded"].(bool); !ok {
		return nil, errp.New("version")
	}
	return deviceInfo, nil
}

// DeviceInfo gets device information.
func (dbb *Device) DeviceInfo() (*DeviceInfo, error) {
	return dbb.deviceInfo(dbb.password)
}

// Ping returns true if the device is initialized, and false if it is not.
func (dbb *Device) Ping() (bool, error) {
	reply, err := dbb.sendPlain("ping", "")
	if err != nil {
		return false, err
	}
	ping, ok := reply["ping"].(string)
	return ok && ping == "password", nil
}

// SetPassword defines a password for the device. This only works on a fresh device. If a password
// has already been configured, a new one cannot be set until the device is reset.
func (dbb *Device) SetPassword(password string) error {
	reply, err := dbb.sendPlain("password", password)
	if err != nil {
		return err
	}
	if reply["password"] != "success" {
		return errp.New("unexpected reply")
	}
	dbb.password = password
	dbb.onStatusChanged()
	return nil
}

// Login validates the password. This needs to be called before using any API call except for Ping()
// and SetPassword(). It returns whether the next login attempt requires a long-touch, and the number
// of remaining attempts.
func (dbb *Device) Login(password string) (bool, string, error) {
	deviceInfo, err := dbb.deviceInfo(password)
	if err != nil {
		var remainingAttempts string
		var needsLongTouch bool
		if dbbErr, ok := err.(*Error); ok {
			groups := regexp.MustCompile(`(\d+) attempts remain before`).
				FindStringSubmatch(dbbErr.Error())
			if len(groups) == 2 {
				remainingAttempts = groups[1]
			}
			needsLongTouch = strings.Contains(dbbErr.Error(), "next")
		}
		return needsLongTouch, remainingAttempts, err
	}
	dbb.password = password
	dbb.seeded = deviceInfo.Seeded
	dbb.onStatusChanged()
	return false, "", nil
}

func stretchKey(key string) string {
	const (
		iterations = 20480
		keylen     = 64
	)
	return hex.EncodeToString(pbkdf2.Key(
		[]byte(key),
		[]byte("Digital Bitbox"),
		iterations,
		keylen,
		sha512.New))
}

func (dbb *Device) seed(devicePassword, backupPassword, source, filename string) error {
	if source != "create" && source != "backup" {
		panic(`source must be "create" or "backup"`)
	}
	key := stretchKey(backupPassword)
	reply, err := dbb.send(
		map[string]interface{}{
			"seed": map[string]string{
				"source":   source,
				"key":      key,
				"filename": filename,
			},
		},
		devicePassword)
	if err != nil {
		return err
	}
	if reply["seed"] != "success" {
		return errp.New("unexpected result")
	}
	return nil
}

func backupFilename(backupName string) string {
	return fmt.Sprintf("%s-%s.pdf", backupName, time.Now().Format("2006-01-02-15-04-05"))
}

// SetName sets the device name. Retrieve the device name using DeviceInfo().
func (dbb *Device) SetName(name string) error {
	if !regexp.MustCompile(`^[0-9a-zA-Z-_ ]{1,31}$`).MatchString(name) {
		return errp.New("invalid wallet name")
	}
	reply, err := dbb.send(
		map[string]interface{}{
			"name": name,
		},
		dbb.password)
	if err != nil {
		return err
	}
	newName, ok := reply["name"].(string)
	if !ok || len(newName) == 0 || newName != name {
		return errp.New("unexpected result")
	}
	return nil
}

// CreateWallet creates a new wallet and stores a backup containing `walletName` in the
// filename. The password used for the backup is the same as the one for the device.
func (dbb *Device) CreateWallet(walletName string) error {
	if !regexp.MustCompile(`^[0-9a-zA-Z-_ ]{1,31}$`).MatchString(walletName) {
		return errp.New("invalid wallet name")
	}
	if err := dbb.seed(
		dbb.password,
		dbb.password,
		"create",
		backupFilename(walletName),
	); err != nil {
		return err
	}
	dbb.seeded = true
	dbb.onStatusChanged()
	return nil
}

// IsErrorAbort returns whether the user aborted the operation.
func IsErrorAbort(err error) bool {
	dbbErr, ok := err.(*Error)
	return ok && (dbbErr.Code == ErrTouchAbort || dbbErr.Code == ErrTouchTimeout)
}

// IsErrorSDCard returns whether the SD card was not inserted during an operation that requires it.
func IsErrorSDCard(err error) bool {
	dbbErr, ok := err.(*Error)
	return ok && dbbErr.Code == ErrSDCard
}

// RestoreBackup restores a backup from the SD card. Returns true if restored and false if aborted
// by the user.
func (dbb *Device) RestoreBackup(backupPassword, filename string) (bool, error) {
	err := dbb.seed(dbb.password, backupPassword, "backup", filename)
	if IsErrorAbort(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	dbb.seeded = true
	dbb.onStatusChanged()
	return true, nil
}

// CreateBackup creates a new backup of the current device seed on the SD card.
func (dbb *Device) CreateBackup(backupName string) error {
	reply, err := dbb.send(
		map[string]interface{}{
			"backup": map[string]string{
				"key":      stretchKey(dbb.password),
				"filename": backupFilename(backupName),
			},
		},
		dbb.password)
	if err != nil {
		return err
	}
	if reply["backup"] != "success" {
		return errp.New("unexpected result")
	}
	return nil
}

// Blink flashes the LED.
func (dbb *Device) Blink(password string) error {
	_, err := dbb.sendKV("led", "abort", password)
	return err
}

// Reset resets the device. Returns true if erased and false if aborted by the user.
func (dbb *Device) Reset() (bool, error) {
	reply, err := dbb.sendKV("reset", "__ERASE__", dbb.password)
	if IsErrorAbort(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if reply["reset"] != "success" {
		return false, errp.New("unexpected reply")
	}
	dbb.password = ""
	dbb.seeded = false
	dbb.initialized = false
	dbb.onStatusChanged()
	return true, nil
}

// XPub returns the extended publickey at the path.
func (dbb *Device) XPub(path string) (*hdkeychain.ExtendedKey, error) {
	getXPub := func() (*hdkeychain.ExtendedKey, error) {
		reply, err := dbb.sendKV("xpub", path, dbb.password)
		if err != nil {
			return nil, err
		}
		xpubStr, ok := reply["xpub"].(string)
		if !ok {
			return nil, errp.New("unexpected reply")
		}
		return hdkeychain.NewKeyFromString(xpubStr)
	}
	// Call the device twice, to reduce the likelihood of a hardware error.
	xpub1, err := getXPub()
	if err != nil {
		return nil, err
	}
	xpub2, err := getXPub()
	if err != nil {
		return nil, err
	}
	if xpub1.String() != xpub2.String() {
		return nil, errp.New("critical: the device returned inconsistent xpubs.")
	}
	return xpub1, nil
}

// Random generates a 16 byte random number, hex encoded.. typ can be either "true" or "pseudo".
func (dbb *Device) Random(typ string) (string, error) {
	if typ != "true" && typ != "pseudo" {
		panic("needs to be true or pseudo")
	}
	reply, err := dbb.sendKV("random", typ, dbb.password)
	if err != nil {
		return "", err
	}
	rand, ok := reply["random"].(string)
	if !ok {
		return "", errp.New("unexpected reply")
	}
	if len(rand) != 32 {
		return "", fmt.Errorf("unexpected length, expected 32, got %d", len(rand))
	}
	return rand, nil
}

// BackupList returns a list of backup filenames.
func (dbb *Device) BackupList() ([]string, error) {
	reply, err := dbb.sendKV("backup", "list", dbb.password)
	if err != nil {
		return nil, err
	}
	filenames, ok := reply["backup"].([]interface{})
	if !ok {
		return nil, errp.New("unexpected reply")
	}
	filenameStrings := []string{}
	for _, filename := range filenames {
		filenameString, ok := filename.(string)
		if !ok {
			return nil, errp.New("unexpected reply")
		}
		filenameStrings = append(filenameStrings, filenameString)
	}
	return filenameStrings, nil
}

// EraseBackup deletes a backup.
func (dbb *Device) EraseBackup(filename string) error {
	reply, err := dbb.send(
		map[string]interface{}{
			"backup": map[string]string{
				"erase": filename,
			},
		},
		dbb.password)
	if err != nil {
		return err
	}
	if reply["backup"] != "success" {
		return errp.New("unexpected result")
	}
	return nil
}

// LockBootloader locks the bootloader.
func (dbb *Device) LockBootloader() error {
	reply, err := dbb.sendKV("bootloader", "lock", dbb.password)
	if err != nil {
		return err
	}
	if val, ok := reply["bootloader"].(string); !ok || val != "lock" {
		return errp.New("unexpected reply")
	}
	return nil
}

// Sign returns signatures for the provided hashes. The private keys used to sign them are derived
// using the provided keyPaths.
func (dbb *Device) Sign(signatureHashes [][]byte, keyPaths []string) ([]btcec.Signature, error) {
	if len(signatureHashes) != len(keyPaths) {
		panic("len of keyPaths must match len of signatureHashes")
	}
	data := []map[string]string{}
	for i, signatureHash := range signatureHashes {
		data = append(data, map[string]string{
			"hash":    hex.EncodeToString(signatureHash),
			"keypath": keyPaths[i],
		})
	}
	cmd := map[string]interface{}{
		"sign": map[string]interface{}{
			"data": data,
		},
	}
	// First call returns the echo.
	_, err := dbb.send(cmd, dbb.password)
	if err != nil {
		return nil, err
	}
	// Second call returns the signatures.
	reply, err := dbb.send(cmd, dbb.password)
	if err != nil {
		return nil, err
	}
	sigs, ok := reply["sign"].([]interface{})
	if !ok {
		return nil, errp.New("unexpected response")
	}
	signatures := []btcec.Signature{}
	for _, sig := range sigs {
		sigMap, ok := sig.(map[string]interface{})
		if !ok {
			return nil, errp.New("unexpected response")
		}
		hexSig, ok := sigMap["sig"].(string)
		if !ok {
			return nil, errp.New("unexpected response")
		}
		if len(hexSig) != 128 {
			return nil, errp.New("unexpected response")
		}
		sigR, ok := big.NewInt(0).SetString(hexSig[:64], 16)
		if !ok {
			return nil, errp.New("unexpected response")
		}
		sigS, ok := big.NewInt(0).SetString(hexSig[64:], 16)
		if !ok {
			return nil, errp.New("unexpected response")
		}
		signatures = append(signatures, btcec.Signature{R: sigR, S: sigS})
	}
	return signatures, nil
}