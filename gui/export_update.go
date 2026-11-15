package gui

// The update flow without a toolkit: what to offer for a release (self
// update, or only the release page and why), the release notes to show,
// and the self update itself with its progress. The Fyne dialogs
// (update_ui.go) and the Gio update page (ui/pages_update.go) both drive
// it, so the two apps check, verify and apply the same way.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// NewUpdateCheck builds a check result by hand (tests, fakes).
func NewUpdateCheck(rel *release.Info, list []release.Info) UpdateCheck {
	return updateCheck{rel: rel, list: list}
}

// Valid reports whether c holds a release (the zero value does not).
func (c updateCheck) Valid() bool { return c.rel != nil }

// Release is the offered release; nil for the zero value.
func (c updateCheck) Release() *release.Info { return c.rel }

// Page is the release's web page, or the releases page.
func (c updateCheck) Page() string {
	if c.rel != nil && c.rel.HTMLURL != "" {
		return c.rel.HTMLURL
	}
	return release.Page
}

// UpdateNotes is the releases whose notes to show for c: every app
// release newer than current, highest first, or the offered release
// alone when current is not behind.
func UpdateNotes(c UpdateCheck, current string) []release.Info {
	if c.rel == nil {
		return nil
	}
	return updateNotes(c, current)
}

// MaxNotes caps the releases whose notes are shown; the rest are a link
// to the releases page away.
const MaxNotes = maxNotes

// CanSelfUpdate is false for "dev"/empty builds: they may check for
// releases but must never overwrite themselves.
func CanSelfUpdate(version string) bool {
	_, ok := release.ParseVersion(version)
	return ok
}

// UpdateBlock says why a release can only be fetched from its page.
type UpdateBlock int

const (
	UpdateCanApply   UpdateBlock = iota // self update possible
	UpdateDevBuild                      // a dev build never overwrites itself
	UpdateUnsigned                      // no signed manifest
	UpdateBadSig                        // the manifest's signature is wrong
	UpdateFetchFail                     // the manifest could not be read
	UpdateMinVersion                    // this version may not update directly
	UpdateNoAsset                       // no exe for this architecture
)

// UpdatePlan is what can be done about one release: apply it in place,
// or open its page (Block says why).
type UpdatePlan struct {
	Tag   string
	Page  string
	Block UpdateBlock
	err   error // UpdateFetchFail

	rel      *release.Info
	m        *release.Manifest
	asset    *release.ManifestAsset
	assetURL string
}

// CanApply reports whether the release can be downloaded and applied.
func (p UpdatePlan) CanApply() bool { return p.Block == UpdateCanApply && p.asset != nil }

// Note is the line that explains a blocked plan, "" when there is none
// (it can be applied, or it is a dev build).
func (p UpdatePlan) Note(l Lang) string { return p.note(func(k string) string { return T(l, k) }) }

func (p UpdatePlan) note(t func(string) string) string {
	switch p.Block {
	case UpdateUnsigned:
		return t("update.unsigned")
	case UpdateBadSig:
		return t("update.badsig")
	case UpdateFetchFail:
		return t("update.checkfailed") + p.err.Error()
	case UpdateMinVersion:
		return fmt.Sprintf(t("update.minversion"), p.m.MinVersion)
	case UpdateNoAsset:
		return t("update.noasset")
	}
	return ""
}

// PlanUpdate decides what to offer for chk's release given the manifest
// fetch result (m and err both nil for a dev build, which only gets the
// release page).
func PlanUpdate(chk UpdateCheck, version string, m *release.Manifest, err error) UpdatePlan {
	p := UpdatePlan{Page: chk.Page(), rel: chk.rel, m: m}
	if chk.rel != nil {
		p.Tag = chk.rel.TagName
	}
	switch {
	case !CanSelfUpdate(version):
		p.Block = UpdateDevBuild
	case errors.Is(err, release.ErrNoManifest):
		p.Block = UpdateUnsigned
	case errors.Is(err, release.ErrBadSignature):
		p.Block = UpdateBadSig
	case err != nil:
		p.Block, p.err = UpdateFetchFail, err
	case m == nil:
		p.Block = UpdateUnsigned
	case !m.Allows(version):
		p.Block = UpdateMinVersion
	default:
		asset, ok := m.AssetFor(runtime.GOARCH)
		if ok {
			p.assetURL = release.AssetURL(chk.rel, asset.Name)
		}
		if !ok || p.assetURL == "" {
			p.Block = UpdateNoAsset
			break
		}
		p.asset = asset
	}
	return p
}

// PrepareUpdate fetches and verifies chk's signed manifest (#66) and
// plans the update. Network: off the window goroutine.
func PrepareUpdate(chk UpdateCheck, version string) UpdatePlan {
	if !CanSelfUpdate(version) || chk.rel == nil {
		return PlanUpdate(chk, version, nil, nil)
	}
	m, err := fetchManifest(chk.rel)
	return PlanUpdate(chk, version, m, err)
}

// UpdateStage is a step of the self update.
type UpdateStage int

const (
	StageDownloading UpdateStage = iota // Done/Total bytes
	StageHash                           // SHA-256 and size against the manifest
	StageVerifying                      // the exe's own version check
	StageApplying                       // the elevated updater (UAC)
)

// UpdateProgress is where the self update is.
type UpdateProgress struct {
	Stage       UpdateStage
	Done, Total int64 // StageDownloading; Total is -1 when unknown
}

// Text is the progress line, as the Fyne dialog showed it.
func (p UpdateProgress) Text(l Lang) string { return p.text(func(k string) string { return T(l, k) }) }

func (p UpdateProgress) text(t func(string) string) string {
	switch p.Stage {
	case StageHash:
		return t("update.checking")
	case StageVerifying:
		return t("update.verifying")
	case StageApplying:
		return t("update.applying")
	}
	switch {
	case p.Total > 0:
		return fmt.Sprintf("%s  %s / %s", t("update.downloading"), formatBytes(p.Done), formatBytes(p.Total))
	case p.Done > 0:
		return fmt.Sprintf("%s  %s", t("update.downloading"), formatBytes(p.Done))
	}
	return t("update.downloading")
}

// Fraction is the download's share done (0…1), -1 when unknown.
func (p UpdateProgress) Fraction() float64 {
	if p.Stage != StageDownloading {
		return 1
	}
	if p.Total <= 0 {
		return -1
	}
	return float64(p.Done) / float64(p.Total)
}

// ApplySelfUpdate downloads plan's asset, checks its SHA-256/size against
// the signed manifest, runs the exe's own version check, then hands over
// to the elevated updater (selfupdate.go). On nil the updater is waiting
// for this process to exit: the caller quits the app. A download error, a
// hash mismatch (IsHashMismatch), a declined UAC prompt or a cancelled ctx
// leave everything as it was. progress (may be nil) is called from this
// goroutine.
func ApplySelfUpdate(ctx context.Context, plan UpdatePlan, progress func(UpdateProgress)) error {
	if !plan.CanApply() {
		return errors.New("update cannot be applied in place")
	}
	report := func(p UpdateProgress) {
		if progress != nil {
			progress(p)
		}
	}
	path, err := downloadUpdate(ctx, plan.assetURL, stagingDir(), plan.rel.TagName, func(done, total int64) {
		report(UpdateProgress{Stage: StageDownloading, Done: done, Total: total})
	})
	if err != nil {
		return err
	}
	// First gate: the bytes must be exactly what the signed manifest
	// promised. Nothing has executed yet.
	report(UpdateProgress{Stage: StageHash})
	if err := verifyDownloadedHash(path, plan.asset); err != nil {
		return err
	}
	report(UpdateProgress{Stage: StageVerifying})
	if err := verifyDownloadedExe(path, plan.rel.TagName); err != nil {
		os.Remove(path)
		return err
	}
	if ctx.Err() != nil {
		os.Remove(path)
		return ctx.Err()
	}
	// The elevated updater re-verifies everything itself from its own
	// admin-only copies (#126), so it gets the signed manifest too.
	report(UpdateProgress{Stage: StageApplying})
	man, sig, err := stageManifest(filepath.Dir(path), plan.m)
	if err != nil {
		return err
	}
	return runElevatedSelf(fmt.Sprintf(`update-apply "%s" %d "%s" "%s"`, path, os.Getpid(), man, sig))
}

// IsHashMismatch reports a download that differs from the manifest.
func IsHashMismatch(err error) bool { return errors.Is(err, errHashMismatch) }

// UpdateFailedText is the message for a failed self update.
func UpdateFailedText(l Lang, err error) string {
	if IsHashMismatch(err) {
		return T(l, "update.hashmismatch")
	}
	return T(l, "update.failed") + err.Error()
}
