package gui

// What the 서비스 관리 page (ui/pages_servicectl.go, #165) needs from the
// Windows side: the service's state, the elevated install / start / stop /
// uninstall, the install-location check and the crash folders. The Fyne
// settings tab calls the same functions.

import (
	"os"

	"github.com/Protomothis/smartthings-pc-control/internal/appid"
)

// ServiceName is the Windows service's name.
const ServiceName = serviceName

// ServiceState is the Windows service's state as the SCM reports it.
type ServiceState = svcState

// Service states.
const (
	ServiceUnknown      = svcUnknown // a start or stop in progress, or no answer
	ServiceNotInstalled = svcNotInstalled
	ServiceStopped      = svcStopped
	ServiceRunning      = svcRunning
)

// QueryServiceState asks the service control manager (no process, no
// admin rights), so a page may ask every few seconds.
func QueryServiceState() ServiceState { return scmServiceState() }

// InstallService installs and starts the service from this exe's folder,
// elevated (UAC), and waits for it. An error is the declined prompt or
// the failed launch. Off the window goroutine.
func InstallService() error { return runElevatedSelfWait("install --gui") }

// StartService starts the installed service, elevated, and waits.
func StartService() error { return runElevatedWait("sc.exe", "start "+serviceName) }

// StopService stops the service, elevated, and waits for sc.exe (the stop
// itself may still be pending when it returns).
func StopService() error { return runElevatedWait("sc.exe", "stop "+serviceName) }

// UninstallService removes the service, elevated, and waits. The Start
// menu shortcut is this user's (the elevated child may run as another
// admin), so it goes here, once the service is really gone. Best effort;
// the next tray start makes it again.
func UninstallService() error {
	if err := runElevatedSelfWait("uninstall"); err != nil {
		return err
	}
	if queryServiceState() == svcNotInstalled {
		appid.RemoveToastShortcut()
	}
	return nil
}

// InstallRisk is why installing from the exe's current folder deserves a
// question first.
type InstallRisk = installDirRisk

// Install risks.
const (
	InstallRiskNone     = installDirOK
	InstallRiskVolatile = installDirVolatile // Downloads, Desktop, Documents, temp
	InstallRiskWritable = installDirWritable // ordinary users can modify it; installing locks it
	InstallRiskUnsafe   = installDirUnsafe   // ordinary users can modify it and it cannot be locked
)

// RecommendedInstallDir is the folder the location question suggests.
const RecommendedInstallDir = recommendedInstallDir

// ExeInstallRisk is the running exe's folder and why it is a questionable
// place to install the service from (InstallRiskNone when unknown).
func ExeInstallRisk() (dir string, risk InstallRisk) { return exeInstallDirRisk() }

// InstallRiskBodyKey is the text key explaining risk (formatted with the
// folder and RecommendedInstallDir), "" for none.
func InstallRiskBodyKey(risk InstallRisk) string { return installDirRiskBodyKey[risk] }

// PrepareCrashDir makes this user's crash folder (it may not exist before
// debug mode has run) and returns it.
func PrepareCrashDir() (string, error) {
	dir := guiCrashDir()
	return dir, os.MkdirAll(dir, 0o755)
}

// ServiceCrashDir is where the service keeps its admin-only crash records.
func ServiceCrashDir() string { return serviceCrashDir() }
