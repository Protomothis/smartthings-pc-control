# 리팩터링·정리 계획 (2026-10-01 감사 결과)

v1.2.0 / Edge 1.1.0 개발로 범위가 크게 늘어난 뒤 코드·테스트·문서·CI·스택을 네 갈래로 감사했다
(서비스, 데스크톱 앱, Edge 드라이버, 저장소 전반). 이 문서는 그 결과를 우선순위별 작업으로 정리한다.

## 0. 한눈에

| 영역 | 규모 | 핵심 진단 |
|---|---|---|
| 서비스 (`service/`, `useraction/`, `internal/`) | 소스 약 1.7만 줄, 테스트 약 1.4만 줄, 테스트 약 440개 | 평평한 `service` 패키지 1개에 전역 변수 87개·뮤텍스 33개. 명령 처리·에러 응답·인증 처리가 3~4벌씩 중복 |
| 데스크톱 앱 (`gui/`) | 소스 8.8천 줄, 테스트 83개 | `ui` 구조체 필드 58개. 저장 경로 4개가 각자 기준값을 갱신. UI 스레드에서 HTTP 호출(최대 수십 초 멈춤) |
| Edge 드라이버 (`edge/`) | 소스 6.7천 줄(바이트의 50%가 주석), 테스트 470개 | 패키지 506KB/한도 655KB. 다음 프로필 세대(v4)를 올리면 한도 초과. poll.lua에 책임 8가지 |
| 저장소·CI | 워크트리 57개, 로컬 브랜치 96개 | Latest 릴리스 충돌로 **자동 업데이트가 멈춤**. 마일스톤 브랜치에서 Go CI가 돌지 않음 |

전체 결론: **스택은 유지**하고, 보안·릴리스 결함을 먼저 고친 뒤, 구조 분리와 테스트 정리는 별도 리팩터링 마일스톤에서 한다.

## 1. 0단계 — 지금 바로 (v1.2.0 공개 전 필수)

실제 결함이거나 보안 문제다. 작은 작업들이다.

| # | 항목 | 근거 | 규모 |
|---|---|---|---|
| 0-1 | **Latest 릴리스 충돌**: GitHub의 Latest가 `edge-v1.0.1`이라 앱 업데이터(`/releases/latest`)가 새 버전을 못 찾는다(9/28 이후 v1.1.2 알림 없음). 즉시 `gh release edit v1.1.2 --latest`, `edge.yml`에 `make_latest: false`, 업데이터는 `/releases` 목록에서 `v`로 시작하고 prerelease가 아닌 것을 고른다 | internal/release/release.go:18 | S |
| 0-2 | **rc 태그가 정식 배포되는 위험**: `release.yml`에 prerelease 처리가 없어 `v1.2.0-rc1`을 올리면 Latest가 되고 서명된 매니페스트로 전원 자동 업데이트된다. `prerelease: ${{ contains(github.ref_name, '-') }}`, 버전 비교에서 rc < 정식 | release.yml, ParseVersion | S |
| 0-3 | **`/api/test/{cmd}`**: GET·CSRF 검사 없음, 유예 없이 `forceshutdown` 즉시 실행. 시크릿이 비어 있으면(기본값) 인증도 통과 → 브라우저에서 연 아무 페이지가 한 줄로 PC를 강제 종료할 수 있다. POST + CSRF + 공통 명령 처리 경로로 | service/webui.go:351 | S |
| 0-4 | **로컬 API 방어**: Host 헤더 허용 목록(127.0.0.1/localhost)으로 DNS 리바인딩 차단, 시크릿 비교를 상수 시간으로, 레거시 `/{secret}/{cmd}`에도 속도 제한 | webui.go, st_api.go:174, server.go:567 | S |
| 0-5 | **COM 호출 래퍼의 unsafe 규칙 위반**: `comObject.call`이 포인터를 일반 함수 인자로 넘겨 스택 이동 시 메모리 오염 가능. `//go:uintptrescapes` 또는 `internal/appid`처럼 SyscallN 직접 호출 | useraction/coreaudio.go:90 (winrt.go 공유) | S |
| 0-6 | **마일스톤 브랜치에서 Go CI 미실행**: `ci.yml` push에 `milestone/**` 추가(지금 44커밋이 vet·test를 안 거쳤다), release 잡에 vet·test 추가, `generate_release_notes` 끔 | ci.yml, release.yml | S |
| 0-7 | **앱에서 포트를 바꾸면 재시작 전까지 연결이 끊김**: Client·포트를 시작 때 한 번만 정한다 | gui/gui.go:211 | S |
| 0-8 | **화면 끄기·켜기가 멈출 수 있음**: `HWND_BROADCAST` + `SendMessage`는 응답 없는 창 하나에 영원히 막히고 고루틴이 샌다 → `SendMessageTimeout` | service/commands.go:107 | S |

## 2. 1단계 — v1.2.0 안정화에 함께 (작은 정리)

| # | 항목 | 규모 |
|---|---|---|
| 1-1 | `.gitattributes`에 `* text=auto`, `*.go *.lua *.yml eol=lf` (지금은 작업본이 CRLF라 로컬 `gofmt -l`이 100개 파일을 잘못 보고) | S |
| 1-2 | CI에 gofmt 검사·golangci-lint(govet, staticcheck, errcheck, gosec, noctx, bodyclose, errorlint, usestdlibvars, misspell). 현재 실제 gofmt 위반 4개 수정 | S |
| 1-3 | Edge CI에 실제 Lua 5.3(`apt install lua5.3`) 실행, `luacheck`, `npm run check-profiles` 추가 (허브는 C Lua, 로컬은 fengari 유지) | S |
| 1-4 | 알림 버스의 전송 간격(1초)을 주입 가능하게 → 서비스 테스트 33초 → 약 8초 예상, 간헐 실패(`TestGraceCallbackEditsThroughPollerOnly`)·`-race` 레이스 5건 해소. `-race`는 주 1회 스케줄 잡 | S |
| 1-5 | GUI 경합: 고루틴에서 위젯 읽기(`logsAuto.Checked`, `awake.sel`), `SendNotification` fyne.Do 밖 호출 | S |
| 1-6 | go-toast 제거: 유예 토스트도 이미 안전한 `useraction` 토스트 경로로(go-toast는 2019년 이후 방치, PowerShell 문자열 삽입 방식) | S |
| 1-7 | PowerShell 호출을 절대 경로 헬퍼 하나로(SYSTEM 권한에서 PATH 탐색 중). suspend는 `SetSuspendState`, lock은 WTS API로 교체 | M |
| 1-8 | 죽은 코드·낡은 주석 정리(staticcheck U1000 등 4건, "PowerShell explorer" 주석, 안 쓰는 i18n 키 3개) | S |
| 1-9 | 저장소 정리: 병합된 워크트리 57개·로컬 브랜치 약 90개·원격 브랜치 9개 삭제, `develop` 제거(main과 차이 0, 역할 없음) → main + 마일스톤 + 짧은 feature 흐름 | S |
| 1-10 | 열린 이슈 16개는 구현 완료 → 릴리스 때 일괄 닫기. 앞으로 main 병합 커밋에 `Closes #n` | S |

## 3. 2단계 — 리팩터링 마일스톤 (v1.3.0 제안)

기능 변경 없이 구조만 바꾼다. 각 작업 전에 동작을 고정하는 테스트를 먼저 만든다.

### 3.1 계약 테스트 먼저 (모든 분리의 안전망)
- `testdata/st-v1/`에 golden JSON(`status.full.json`, `status.minimal-1.0.json`, `command.*.json`, `push.*.json`)을 두고
  **Go 핸들러 출력과 Lua 드라이버 입력이 같은 파일을 쓴다.** 지금은 Go 구조체와 Lua 테스트의 손으로 만든 본문이 따로 놀아
  필드 이름이 바뀌어도 양쪽 테스트가 모두 통과한다.
- `/api/*` 응답도 같은 방식으로 고정한다.

### 3.2 서비스
- 패키지 분할 (위→아래 의존): `internal/config`(타입·로드·저장·마이그레이션·검증, 설정 폴더 주입) → `internal/logx`(slog)
  → `service/devstate`(audio·media·battery·display·session 저장소를 제네릭 하나로) → `service/session`(사용자 세션 실행)
  → `service/stapi`, `service/webui` → `service/tgcontrol` → `service/power`(명령·예약·유예·awake, 가장 얽혀 있어 마지막).
  루트 `service`는 조립만.
- 중복 통합: `dispatchCommand(name, from, origin, mode)` 하나(지금 3벌), 에러 응답 한 형태(지금 4종), 인증·CSRF 미들웨어
  (`checkAuth` 보일러플레이트 19회), 에러→HTTP 매핑 하나(지금 3벌, 하나는 내부 에러 문자열을 그대로 노출),
  속도 제한기 하나(지금 4종), `configChangedKeys`는 json 태그에서 자동 생성.
- 전역 상태 축소: 테스트용 교체 함수 변수 약 20개 → 의존성 구조체로. 테스트가 바이너리 옆에 `config.json`을 쓰지 않게.

### 3.3 데스크톱 앱
- **저장 조정자**: 탭마다 `formTab{Fill, Dirty, ApplyTo}`(설정 탭만 아직 모델이 없음)와 하나의 저장 경로.
  저장 후 항상 다시 받아오고 모든 탭의 변경 표시를 다시 계산, 재연결·언어 변경이 저장 안 한 편집을 덮지 않게.
  **탭 사이 병합에서 시크릿·마스킹 토큰 보존 테스트**(과거 시크릿 삭제 버그 유형).
- **비동기 헬퍼**: 저장·명령·예약·재시작·로그인의 HTTP 호출을 UI 스레드 밖으로(지금 최대 약 40초 멈춤 가능).
- 폴링은 창이 보일 때만(로그는 로그 탭일 때만). 시작 시 글꼴 26MB 동기 로드를 지연 로드로.
- `gui.go`(1658줄)를 탭별 파일로, 서비스 클라이언트를 인터페이스 뒤로.
- i18n을 `locales/ko.json`·`en.json`(go:embed)으로, 키·서식 동사 일치 테스트.
- **탭 재구성**(위 작업 뒤): 명령·예약·프리셋·**공유**(세션 정보·사용자 이름·재생 정보·실행 중 앱 감지)·SmartThings·텔레그램·설정·로그.
  측정상 8탭은 한국어로도 경계(637/632px), 영어는 이미 7탭에서 넘친다 → 탭 아이콘 제거 또는 창 폭 800.

### 3.4 Edge 드라이버
- **주석 제거 빌드**: `tools/build.js`가 줄 수를 보존한 채 주석을 빈 줄로 바꿔 `build/edge/`에 쓰고 CI는 그 폴더를 패키징
  (src 277KB → 133KB, 허브 로그의 줄 번호는 그대로, 주석 뗀 소스로 470개 테스트 통과 확인). 크기 테스트는 빌드 결과 기준으로.
- 프로필 생성기가 템플릿 주석을 떼고 설정 설명을 줄인다(세대당 약 50KB 절감). 공개 후 이전 완료가 확인되면 v1 은퇴.
- 모듈 정리: `device/fields.lua`(필드 이름·저장 단일화, 리터럴 중복 제거), `device/emit.lua`(강제 전송 규칙 7가지를 사유 기반 API 하나로),
  `device/rows.lua`(쉬는 값 줄 공통 처리), `handlers/*`(init.lua 분리), `model/*`(state.lua 분리), 순환 require 제거(의존성 주입).
- `ROWS_VERSION`은 `profiles.VERSION`에서 파생.
- 주석: 플랫폼 규칙은 1~2줄 + 플랫폼 노트 링크, 이력("#88 전에는…")은 git으로.

### 3.5 테스트 정리

| 범위 | 지금 | 목표 | 정리 대상 |
|---|---|---|---|
| 서비스 Go | 약 440 | 약 400 | 기본값 테스트 8개 → 기본 설정 golden 하나, 기능별 status 블록 테스트 약 8개 → status golden 하나, 텔레그램 상태 줄 5개 → 하나, 상수·인자 나열 테스트 |
| 데스크톱 앱 Go | 83 | 약 85 | 번역 문장 통째 비교·개수 하드코딩 제거, 대신 저장 병합·변경 표시 테스트 추가 |
| Edge Lua | 470 | 약 410 | 생성기 규칙을 Lua로 재구현한 테스트(CI의 check-profiles로 대체), 한글 문자열 단언 66개 → golden 약 10개 + i18n 키 비교, en/ko 쌍둥이 병합, 상수 고정 테스트 |

**반드시 남길 것**: 외부 계약(`/st/v1`, 푸시, 설정 마이그레이션), 보안 경계(인증, 시크릿 유출 방지, 셸 해석 금지),
상태 기계(유예·예약·전환·awake·로그인), 파서(M-SEARCH, 종료 사유), 그리고 Edge 플랫폼 규칙을 담은 테스트
(목록 인자는 문자열, 닫힌 목록 무동작, 빈 문자열 금지, 첫 전송 강제, 패키지 크기).

**테스트 정책**: 버그를 고치면 회귀 테스트 1개 필수. Fyne 레이아웃 세부, 문서 문구, Win32 호출 자체는 테스트하지 않는다
(인터페이스 뒤로 숨기고 실측 체크리스트로).

### 3.6 문서
- 설계 문서는 결정과 근거만, 출시 후 동결(300줄 이하). 진행 메모("실측 대기")는 이슈로. 플랫폼 함정은 `edge-platform-notes.md` 한 곳.
- 사용자 문서의 정본은 Wiki. README 150줄, edge/README 200줄 상한, 문장 단위 줄바꿈.
- CHANGELOG 항목은 사용자 관점 1~2문장(약 150자) + `(#n)`. 구현 세부는 커밋 본문으로. (지금 Unreleased 56개 항목, 평균 333자)
- 커밋 규칙을 CONTRIBUTING 한 단락으로: 스코프 목록(`service` `gui` `edge` `docs` `ci` …), 제목 50자 이하.

## 4. 스택 평가

| 항목 | 판단 | 이유 |
|---|---|---|
| Go (서비스) | 유지 | 단일 exe, Windows 서비스·API 바인딩에 적합 |
| Fyne (데스크톱 앱) | **유지, v2.0에서 재검토** | 안정적으로 동작하고 상태 모델 분리가 이미 시작됨. 단점: cgo/MinGW 필요, OpenGL 필요(RDP·GPU 없는 VM 위험), 스위치·접근성 약함 |
| WebUI (`service/web/settings.html`) | **원격 접속용으로 축소** (2026-10-01 결정) | 정본 설정 화면은 데스크톱 앱. WebUI는 다른 기기의 브라우저에서 꼭 필요한 것(상태, 전원 명령, 예약, 핵심 설정)만 컴팩트하게 남기고 나머지는 제거. v1.3.0 작업 |
| PowerShell 호출(14곳) | 단계적 제거 | 느리고(호출당 약 1초), SYSTEM에서 PATH 탐색. Windows API로 대체 |
| go-toast | 제거 | 방치됨, 문자열 삽입 방식 |
| go-ole | **제거** (2026-10-01) | SAPI(소리내어 읽기)에서만 썼는데 그 기능을 없앴다. 남은 COM 호출(Core Audio·WinRT)은 vtable 직접 호출 |
| Lua Edge 드라이버 | 유지(필수) | SmartThings 요구 |
| fengari + bun (Lua 테스트) | 유지 + 실제 Lua CI 추가 | 로컬은 빠르고 충분, 허브와의 차이는 CI의 lua5.3이 잡는다 |
| 릴리스 파이프라인(서명 매니페스트) | 유지 | 0-1, 0-2만 고치면 된다. Authenticode 서명(SmartScreen 경고 제거)은 비용 대비 별도 결정 |
| 브랜치 흐름 | 단순화 (완료) | develop 제거, main + 마일스톤 + feature. 병합된 워크트리 54개·로컬 브랜치 91개·원격 브랜치 8개 정리(2026-10-01) |

## 5. 권장 순서

1. 0단계 전부 → v1.2.0 rc에 포함해 사용자 테스트 후 릴리스
2. 1단계는 v1.2.0에 같이 넣되, 1-7(PowerShell 교체)은 범위가 크면 v1.3.0으로
3. v1.3.0 리팩터링 마일스톤: 계약 테스트 → 서비스 분할 → 앱 저장 조정자·비동기 → 탭 재구성 → Edge 빌드·모듈 정리 → 테스트 정리 → 문서 정리
4. v2.0 검토: 데스크톱 앱 스택(WebView2 통합) PoC
