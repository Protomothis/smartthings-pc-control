# 기여 안내

## 브랜치

- `main`은 릴리스된 코드다. 릴리스 태그(`vX.Y.Z`, `edge-vX.Y.Z`)는 여기서만 단다.
- 한 릴리스의 작업은 `milestone/vX.Y.Z` 브랜치에 모은다. CI는 `main`과 `milestone/**` 푸시, `main`으로 가는 PR에서 돈다.
- 작업마다 마일스톤 브랜치에서 짧은 브랜치를 딴다(`feat/123-…`, `fix/…`, `edge/…`, `gui/…`, `docs/…`, `test/…`, `ci/…`). 끝나면 `Merge <브랜치> into milestone/vX.Y.Z (#n)`로 합치고 지운다.
- 마일스톤은 사용자 테스트가 끝난 뒤 `main`에 합치고 태그를 단다. rc 태그(`-rc1`)는 시험판으로 나간다.

## 커밋

- 제목은 `<스코프>: <한국어 제목>`, 50자 이하, 마침표 없이 무엇을 하는지 쓴다. 예: `service: 잠기지 않은 사용자 세션을 대상으로 고른다`
- 스코프: `service` `gui` `ui` `edge` `docs` `ci` `test` `internal` `useraction`. 둘에 걸치면 `gui/useraction:`처럼 쓴다.
- 본문에는 **왜** 바꾸는지를 쓴다. 실측 결과, 버린 대안, 남은 위험도 여기에 둔다. 관련 이슈는 `(#n)`으로 적는다.
- AI 도구의 도움을 받았으면 본문 끝에 `Co-Authored-By:` 트레일러를 붙인다.
- 사용자에게 보이는 변경은 `CHANGELOG.md`(Windows 앱) 또는 `edge/CHANGELOG.md`(드라이버)의 `[Unreleased]`에 한두 문장으로 적는다. 구현 세부는 커밋 본문에 둔다.

## 테스트

Go 쪽은 cgo 없이 빌드된다(v1.2.4의 Gio UI부터, #165). C 컴파일러는 `go test -race`와 아래 리소스 재생성에만 필요하다.

```bash
gofmt -l .                                         # 비어 있어야 한다
CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
golangci-lint run ./...                            # 설정은 .golangci.yml
go test ./service -run TestContract -update        # /st/v1 계약을 바꿨을 때 testdata/st-v1 다시 쓰기
CGO_ENABLED=0 go build -ldflags="-s -w -H=windowsgui -X main.Version=dev" -o smartthings-pc-control.exe .
```

데스크톱 앱(`ui/`, Gio) 화면은 창 없이 그려 PNG로 본다. 화면을 바꿨으면 해당 화면을 다시 찍어 확인한다.

```bash
STPC_SHOTS_DIR=/tmp/shots go test -run Shots ./ui/
```

앱 로고의 원본은 `assets/logo/*.svg`다(`logo.svg`가 디자인, `logo-16/-20/-24/-32.svg`는 그 크기의 픽셀에 맞춘 변형). 로고를 바꿨으면 래스터를 모두 다시 만든다: `assets/logo/png`, `icon.ico`, `gui/icon.png`, `gui/tray.ico`. `-social`은 `docs/social-preview.png`도 다시 그리고(Windows 글꼴 Segoe UI·맑은 고딕을 쓴다), `-contact`는 크기별 확인용 시트를 쓴다. 그다음 아래처럼 .syso를 다시 만든다.

```bash
go run ./internal/tools/logo -social -contact /tmp/contact.png
```

exe의 아이콘과 애플리케이션 매니페스트(asInvoker, 지원 OS)는 `rsrc_windows_amd64.syso`에 들어 있다. `icon.ico`나 `smartthings-pc-control.manifest`를 바꾸면 MinGW의 windres로 다시 만들고 함께 커밋한다(`TestResourcesCurrent`가 확인한다).

```bash
windres -O coff -F pe-x86-64 -i rsrc.rc -o rsrc_windows_amd64.syso
```

### 설치 프로그램 (#166)

릴리스의 exe와 설치 프로그램은 `scripts/build-windows.ps1`이 `dist\`에 만든다. 설치 프로그램에는 [Inno Setup 6](https://jrsoftware.org/isinfo.php)이 필요하다(`winget install JRSoftware.InnoSetup`).

```powershell
powershell -ExecutionPolicy Bypass -File scripts\build-windows.ps1 -Version 1.2.4-dev.1   # dist\smartthings-pc-control.exe, dist\SmartThingsPCControl-Setup-1.2.4-dev.1.exe
scripts\build-windows.ps1 -Stage exe        # exe만 (-Stage installer는 dist\의 exe로 설치 프로그램만)
```

- 스크립트는 `installer/smartthings-pc-control.iss`다. 설치 로직(서비스 · 방화벽 · 폴더 권한)은 exe의 `install`/`uninstall` 하나로 두고, 스크립트는 파일 복사 · 서비스와 트레이 중지 · 시작 메뉴 · 앱 목록만 맡는다.
- `AppId`는 바꾸지 않는다. 앱이 이 값의 제거 키로 설치 프로그램 설치본을 알아보고(`gui/installsource.go`) 새 설치 프로그램으로 업데이트한다. `TestInstallerScriptAgrees`가 AppId · AUMID · 시작 메뉴 바로가기를 앱과 맞춰 본다.
- 설치 안내 페이지는 `installer/privacy.{ko,en}.txt`(UTF-8 BOM, CRLF). 앱이 밖으로 보내는 요청이 바뀌면 같이 고친다.
- 릴리스 워크플로를 손으로 돌리면(`gh workflow run release.yml --ref <브랜치> -f version=1.2.4-dev.1`) 릴리스 없이 빌드만 하고 결과를 아티팩트로 남긴다. 저장소 변수 `SIGNPATH_ORGANIZATION_ID`가 있으면 exe와 설치 프로그램을 SignPath로 서명한다(설정은 `.signpath/`).

계약 golden(`testdata/st-v1`)은 Go와 Lua 테스트가 함께 쓴다. `-update`로 다시 썼으면 Lua 테스트도 돌린다.

```bash
cd edge
bun tools/lua.js tests/run.lua          # Lua 5.3 테스트 (npm install && npm test 도 같다)
bun tools/lua.js tests/syntax.lua       # 전 모듈 컴파일
node tools/gen-profiles.js --check      # 생성된 프로필이 템플릿과 같은지
```

무엇을 테스트하나:

- **버그를 고치면 회귀 테스트 1개**를 같은 커밋에 넣는다.
- 반드시 지키는 것: 외부 계약(`/st/v1`, 푸시, 설정 마이그레이션 — 가능하면 golden), 보안 경계(인증, 시크릿 유출, CSRF·Host, 셸 해석 금지), 상태 기계(유예·예약·전환·awake·로그인), 파서, Edge 플랫폼 규칙.
- 하지 않는 것: 상수·인자 목록을 그대로 다시 적는 단언, 번역 문장 통째 비교(키 존재·서식 동사 일치와 golden 몇 개로), 개수 하드코딩, 생성기를 다시 구현한 비교(`check-profiles`가 본다), 화면 레이아웃 세부(화면은 아래 스크린샷으로 본다), Win32 호출 자체.
- 같은 모양의 경우가 여럿이면 테스트 함수 여러 개 대신 표 하나로. 루트 `service` 패키지의 통합 테스트가 표면(`stapi`·`webui`·`tgcontrol`)만의 동작을 보려고 테스트 전용 메서드를 늘리지 않는다 — 그 패키지에서 가짜 `Deps`로 돈다.

## Edge 드라이버 빌드와 배포

```bash
cd edge
node tools/build.js && smartthings edge:drivers:package build/edge
smartthings edge:channels:assign <driverId> <version> --channel <channelId>
```

패키지는 주석을 뗀 `build/edge/`로 만든다(커밋하지 않는다).
화면을 바꾸면 프로필 버전을, capability 정의를 바꾸면 id를 새로 해야 한다. 규칙은 [`edge/README.md`](edge/README.md)와 [`docs/design/edge-driver.md`](docs/design/edge-driver.md) §6.6에 있다.

### 릴리스 순서

허브는 채널에 배정된 드라이버를 자동으로 받지만 PC 앱은 사용자가 올린다. 드라이버가 가리키는 앱이 먼저 나가 있어야 한다(edge-driver.md §10.1 "버전 짝 맞춤").

1. 드라이버가 새 서비스 기능을 쓰면 `edge/src/features.lua`의 `RECOMMENDED_SERVICE_VERSION`을 그 서비스 버전으로 올린다(그 기능을 쓰는 커밋에서).
2. **앱 릴리스 먼저** — `vX.Y.Z` 태그와 GitHub Release.
3. **GitHub Latest가 그 앱 릴리스인지 확인**한다. 앱의 업데이트 확인과 드라이버의 "(최신 vX.Y.Z)"가 이것을 본다.
4. 그다음 드라이버를 공개 채널에 배정한다(`edge-vX.Y.Z`). 허브는 자동으로 업데이트되고, 낮은 PC 앱을 쓰는 사용자는 상태 줄에서 "앱 업데이트 필요"를 본다.

## 문서

- 사용자 안내의 정본은 [Wiki](https://github.com/Protomothis/smartthings-pc-control/wiki)다. README는 150줄, `edge/README.md`는 200줄 안으로 둔다.
- `docs/design/`은 결정과 근거만 담고, 릴리스 뒤에는 고치지 않는다(300줄 이하). 진행 메모와 실측 대기는 이슈로 옮긴다.
- SmartThings 플랫폼의 함정은 [`docs/design/edge-platform-notes.md`](docs/design/edge-platform-notes.md) 한 곳에 적는다.
