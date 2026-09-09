package gui

import (
	"strings"

	"fyne.io/fyne/v2/lang"
)

type Lang string

const (
	LangKo Lang = "ko"
	LangEn Lang = "en"
)

// systemLang returns the OS display language, mapped to a supported Lang.
func systemLang() Lang {
	if strings.HasPrefix(strings.ToLower(lang.SystemLocale().String()), "ko") {
		return LangKo
	}
	return LangEn
}

var messages = map[string]map[Lang]string{
	"tab.settings":               {LangKo: "설정", LangEn: "Settings"},
	"tab.commands":               {LangKo: "명령", LangEn: "Commands"},
	"tab.schedule":               {LangKo: "예약", LangEn: "Schedule"},
	"tab.network":                {LangKo: "네트워크", LangEn: "Network"},
	"tab.logs":                   {LangKo: "로그", LangEn: "Logs"},
	"status.connecting":          {LangKo: "연결 중...", LangEn: "Connecting..."},
	"status.connected":           {LangKo: "서비스 연결됨", LangEn: "Service connected"},
	"status.unreachable":         {LangKo: "서비스 연결 안 됨 (포트 5002)", LangEn: "Service unreachable (port 5002)"},
	"status.loadfail":            {LangKo: "설정을 불러오지 못했습니다: ", LangEn: "Failed to load config: "},
	"settings.service":           {LangKo: "서비스 설정", LangEn: "Service Settings"},
	"settings.tools":             {LangKo: "도구", LangEn: "Tools"},
	"settings.port":              {LangKo: "포트", LangEn: "Port"},
	"settings.secret":            {LangKo: "시크릿", LangEn: "Secret"},
	"settings.save":              {LangKo: "저장", LangEn: "Save"},
	"settings.saved":             {LangKo: "저장 완료", LangEn: "Saved"},
	"settings.invalidport":       {LangKo: "잘못된 포트: ", LangEn: "Invalid port: "},
	"settings.openwebui":         {LangKo: "WebUI 열기 (브라우저)", LangEn: "Open WebUI (browser)"},
	"settings.remote":            {LangKo: "WebUI 브라우저 접속 허용 (로컬+LAN)", LangEn: "Allow browser WebUI access (local+LAN)"},
	"settings.remote.hint":       {LangKo: "시크릿 필요, 서비스 재시작 후 적용", LangEn: "Requires a secret; applies after a service restart"},
	"settings.remote.needsecret": {LangKo: "브라우저 접속을 켜려면 시크릿을 먼저 설정하세요", LangEn: "Set a secret before enabling browser access"},
	"settings.grace":             {LangKo: "원격 명령 유예", LangEn: "Remote grace"},
	"settings.grace.off":         {LangKo: "사용 안 함 (즉시 실행)", LangEn: "Off (run immediately)"},
	"settings.grace.hint":        {LangKo: "SmartThings의 종료/재시작/절전/최대절전 명령을 이 시간 뒤에 실행하며, 대기 중 알림·트레이·예약 탭에서 취소할 수 있습니다. 강제 종료와 이 앱의 명령은 항상 즉시 실행됩니다.", LangEn: "Remote shutdown/restart/suspend/hibernate wait this long and can be cancelled from the toast, tray menu or Schedule tab. Force shutdown and this app's commands run immediately."},
	"settings.app":               {LangKo: "앱", LangEn: "App"},
	"duration.sec":               {LangKo: "%d초", LangEn: "%d sec"},
	"duration.min":               {LangKo: "%d분", LangEn: "%d min"},
	"notify.grace.title":         {LangKo: "전원 명령 예약됨", LangEn: "Power command scheduled"},
	"notify.grace.body":          {LangKo: "'%s'이(가) %s 후 실행됩니다. 취소하려면 트레이 메뉴 또는 앱의 예약 탭을 사용하세요.", LangEn: "'%s' runs in %s. Cancel from the tray menu or the app's Schedule tab."},
	"toast.runnow":               {LangKo: "바로 실행", LangEn: "Run now"},
	"toast.cancel":               {LangKo: "취소", LangEn: "Cancel"},
	"settings.restart":           {LangKo: "서비스 재시작", LangEn: "Restart Service"},
	"settings.restart.confirm":   {LangKo: "서비스를 재시작할까요?\n포트 변경은 재시작 후 적용됩니다.", LangEn: "Restart the service?\nPort changes apply after a restart."},
	"settings.restarting":        {LangKo: "서비스 재시작 중...", LangEn: "Service restarting..."},
	"cmd.group.safe":             {LangKo: "일반", LangEn: "General"},
	"cmd.group.power":            {LangKo: "전원 (주의)", LangEn: "Power (careful)"},
	"cmd.immediate.note":         {LangKo: "이 탭의 명령은 즉시 실행됩니다. 유예는 SmartThings 원격 명령에만 적용됩니다.", LangEn: "Commands on this tab run immediately. The grace period applies only to remote SmartThings commands."},
	"cmd.confirm.title":          {LangKo: "확인", LangEn: "Confirm"},
	"cmd.confirm.body":           {LangKo: "지금 이 PC에서 '%s' 명령을 실행할까요?", LangEn: "Run '%s' on this PC now?"},
	"cmd.sent":                   {LangKo: "명령 전송됨: %s", LangEn: "Command sent: %s"},
	"cmd.ping":                   {LangKo: "핑", LangEn: "Ping"},
	"cmd.lock":                   {LangKo: "잠금", LangEn: "Lock"},
	"cmd.screenoff":              {LangKo: "화면 끄기", LangEn: "Screen Off"},
	"cmd.suspend":                {LangKo: "절전", LangEn: "Suspend"},
	"cmd.hibernate":              {LangKo: "최대 절전", LangEn: "Hibernate"},
	"cmd.restart":                {LangKo: "재시작", LangEn: "Restart"},
	"cmd.shutdown":               {LangKo: "종료", LangEn: "Shutdown"},
	"cmd.forceshutdown":          {LangKo: "강제 종료", LangEn: "Force Shutdown"},
	"schedule.command":           {LangKo: "명령", LangEn: "Command"},
	"schedule.delay":             {LangKo: "지연", LangEn: "Delay"},
	"schedule.start":             {LangKo: "예약 시작", LangEn: "Start Schedule"},
	"schedule.cancel":            {LangKo: "예약 취소", LangEn: "Cancel Schedule"},
	"schedule.none":              {LangKo: "활성화된 예약이 없습니다", LangEn: "No active schedule"},
	"schedule.countdown":         {LangKo: "'%s' 실행까지 %s 남음", LangEn: "'%s' runs in %s"},
	"schedule.for":               {LangKo: "'%s' 실행 예정 — 취소하려면 [예약 취소]", LangEn: "'%s' is scheduled — [Cancel Schedule] to abort"},
	"schedule.idle":              {LangKo: "--:--", LangEn: "--:--"},
	"network.refresh":            {LangKo: "새로고침", LangEn: "Refresh"},
	"network.externalip":         {LangKo: "외부 IP", LangEn: "External IP"},
	"network.wolready":           {LangKo: "WoL 사용 가능 (켜진 어댑터 중 WoL 활성)", LangEn: "WoL ready (an active adapter has WoL enabled)"},
	"network.wolnotready":        {LangKo: "WoL 비활성 — 어댑터 전원 관리 설정을 확인하세요", LangEn: "WoL not ready — check adapter power management"},
	"network.adapter.up":         {LangKo: "켜짐", LangEn: "Up"},
	"network.adapter.down":       {LangKo: "꺼짐", LangEn: "Down"},
	"network.wol.on":             {LangKo: "WoL 켜짐", LangEn: "WoL on"},
	"network.wol.off":            {LangKo: "WoL 꺼짐", LangEn: "WoL off"},
	"network.loading":            {LangKo: "네트워크 정보 불러오는 중...", LangEn: "Loading network info..."},
	"logs.autorefresh":           {LangKo: "자동 새로고침 (3초)", LangEn: "Auto-refresh (3s)"},
	"logs.refresh":               {LangKo: "새로고침", LangEn: "Refresh"},
	"logs.empty":                 {LangKo: "로그가 없습니다", LangEn: "No logs"},
	"login.title":                {LangKo: "로그인", LangEn: "Login"},
	"login.secret":               {LangKo: "시크릿", LangEn: "Secret"},
	"login.ok":                   {LangKo: "로그인", LangEn: "Login"},
	"login.cancel":               {LangKo: "취소", LangEn: "Cancel"},
	"lang.label":                 {LangKo: "언어", LangEn: "Language"},
	"update.title":               {LangKo: "업데이트 가능", LangEn: "Update Available"},
	"update.body":                {LangKo: "새 버전 %s이(가) 있습니다 (현재 %s).", LangEn: "Version %s is available (current: %s)."},
	"update.open":                {LangKo: "다운로드 페이지 열기", LangEn: "Open download page"},
	"update.later":               {LangKo: "나중에", LangEn: "Later"},
	"update.check":               {LangKo: "시작 시 업데이트 자동 확인", LangEn: "Check for updates on startup"},
	"autostart.check":            {LangKo: "로그인 시 트레이에 자동 시작", LangEn: "Start in tray at login"},
	"tray.open":                  {LangKo: "열기", LangEn: "Open"},
	"tray.exit":                  {LangKo: "종료", LangEn: "Exit"},
	"svc.section":                {LangKo: "서비스 관리", LangEn: "Service Management"},
	"svc.state.running":          {LangKo: "서비스 실행 중", LangEn: "Service running"},
	"svc.state.stopped":          {LangKo: "서비스 중지됨", LangEn: "Service stopped"},
	"svc.state.notinstalled":     {LangKo: "서비스가 설치되지 않았습니다", LangEn: "Service not installed"},
	"svc.install":                {LangKo: "설치", LangEn: "Install"},
	"svc.uninstall":              {LangKo: "제거", LangEn: "Uninstall"},
	"svc.start":                  {LangKo: "시작", LangEn: "Start"},
	"svc.uninstall.confirm":      {LangKo: "서비스를 제거할까요?\nSmartThings에서 더 이상 이 PC를 제어할 수 없게 됩니다.", LangEn: "Uninstall the service?\nSmartThings will no longer be able to control this PC."},
	"svc.uac.hint":               {LangKo: "UAC 승인 창이 표시됩니다. 완료 후 몇 초 뒤 상태가 갱신됩니다.", LangEn: "A UAC prompt will appear. Status refreshes a few seconds after completion."},
	"logs.filter":                {LangKo: "필터…", LangEn: "Filter…"},
	"logs.nomatch":               {LangKo: "필터와 일치하는 로그가 없습니다", LangEn: "No log lines match the filter"},
	"logs.openfile":              {LangKo: "로그 파일 열기", LangEn: "Open log file"},
	"logs.openfolder":            {LangKo: "폴더 열기", LangEn: "Open folder"},
	"logs.notfound":              {LangKo: "로그 파일이 없습니다: %s", LangEn: "Log file not found: %s"},
	"svc.location.title":         {LangKo: "설치 위치 확인", LangEn: "Check Install Location"},
	"svc.location.body":          {LangKo: "현재 실행 파일 위치:\n%s\n\n서비스는 이 경로의 실행 파일을 가리키며 config.json과 service.log도 같은 폴더에 생성됩니다. 다운로드·바탕 화면·문서·임시 폴더는 나중에 정리되거나 이동되기 쉬워, 설치 후 파일을 옮기면 서비스가 동작하지 않습니다.\n\n권장: 먼저 실행 파일을 %s 같은 고정된 폴더로 옮긴 뒤 설치하세요.", LangEn: "Current executable location:\n%s\n\nThe service points at the executable in this path, and config.json and service.log are created in the same folder. Downloads, Desktop, Documents and temp folders tend to get cleaned up or moved, and moving the file after installation breaks the service.\n\nRecommended: move the executable to a permanent folder such as %s first, then install."},
	"svc.location.anyway":        {LangKo: "여기에 설치", LangEn: "Install here anyway"},
	// Self update (#40 stage 2)
	"update.now":         {LangKo: "지금 업데이트", LangEn: "Update now"},
	"update.manual":      {LangKo: "업데이트 확인", LangEn: "Check for updates"},
	"update.releasepage": {LangKo: "릴리스 페이지 열기", LangEn: "Open release page"},
	"update.noasset":     {LangKo: "이 릴리스에는 자동 업데이트용 exe가 없습니다. 릴리스 페이지에서 직접 다운로드하세요.", LangEn: "This release has no auto-update exe. Download it from the release page."},
	"update.uptodate":    {LangKo: "최신 버전입니다 (%s).", LangEn: "You are up to date (%s)."},
	"update.checkfailed": {LangKo: "업데이트 확인 실패: ", LangEn: "Update check failed: "},
	"update.downloading": {LangKo: "업데이트 다운로드 중...", LangEn: "Downloading update..."},
	"update.verifying":   {LangKo: "다운로드한 파일 확인 중...", LangEn: "Verifying download..."},
	"update.applying":    {LangKo: "UAC 승인 창이 표시됩니다. 앱이 종료된 뒤 서비스를 중지하고 exe를 교체한 후 새 버전으로 다시 실행됩니다.", LangEn: "A UAC prompt will appear. The app closes, the service is stopped, the exe is replaced and the new version relaunches."},
	"update.cancel":      {LangKo: "취소", LangEn: "Cancel"},
	"update.failed":      {LangKo: "업데이트 실패: ", LangEn: "Update failed: "},
}

// T returns the message for key in the given language.
func T(l Lang, key string) string {
	if m, ok := messages[key]; ok {
		if s, ok := m[l]; ok {
			return s
		}
	}
	return key
}
