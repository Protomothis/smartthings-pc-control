# Changelog

## [v0.3.3]

### 새 기능
- **로그인 시 트레이 자동 시작** — 사용자 로그인 시 앱이 창 없이 트레이에만 상주 (`gui --minimized`). 5분 유예 알림([바로 실행]/[취소])은 트레이 앱이 떠 있어야 표시되므로 기본 켬. 설정 탭 도구 → "로그인 시 트레이에 자동 시작"으로 끄기 가능. HKCU Run 등록이라 관리자 권한 불필요, 앱 시작마다 현재 exe 경로로 갱신 (#42)

### 업그레이드 안내 (v0.3.2 → v0.3.3)
- exe 교체 후 앱을 한 번 실행하면 자동 시작이 등록됩니다 (기존 설정은 그대로)

## [v0.3.2]

### 새 기능
- **네이티브 데스크톱 앱 (Fyne)** — 설정/명령/예약/네트워크/로그 5개 탭, WebUI와 동등 기능 (#33)
- **시스템 트레이 상주** — 왼쪽 클릭 = 창 열기, 우클릭 메뉴(열기/상태/빠른 명령/예약 취소/WebUI/종료), 창 닫기 = 트레이로 최소화 (#34)
- **원격 전원 명령 5분 유예** — SmartThings의 종료/재시작/절전/최대절전 명령을 5분 뒤 실행. Windows 알림의 [바로 실행]/[취소] 버튼, 트레이 메뉴, 앱 예약 탭에서 처리 가능. 앱/WebUI에서 직접 실행하는 명령과 강제 종료는 항상 즉시. `shutdown_grace` 설정 (기본 true) (#41)
- **업데이트 자동 확인** — 시작 시 + 매일 GitHub Releases 확인, 새 버전 알림 (#40 1단계)
- **WebUI 브라우저 접속 토글** — `webui_remote` 설정 (기본 false). 켜면 로컬+LAN에서 브라우저 접속 가능 (시크릿 필수, 방화벽 규칙 자동 관리), 끄면 데스크톱 앱 전용 (#38)
- 한국어/영어 지원 (OS 언어 자동 감지 + 전환, 맑은고딕 시스템 폰트 사용)
- 서비스 관리를 앱에 내장 — 설치/시작/제거 (UAC 승격), 상태 자동 감지 (#35, #36)

### 변경
- **더블클릭 = 네이티브 앱 실행** (기존: PowerShell WPF 관리 패널 → 제거됨)
- **브라우저 WebUI는 기본 비활성** — 설정에서 "브라우저 접속 허용"을 켜야 사용 가능
- 빌드에 `-H=windowsgui` 적용 — GUI 실행 시 콘솔창 없음 (CLI 출력은 유지)
- 창 중복 실행 방지 — 재실행 시 기존 창을 앞으로 가져옴
- 로그 로테이션 기준 1MB → 512KB (백업 3개 유지, 총 ~2MB 상한)

### 수정
- 콘솔 모드에서 WebUI 포트가 잘못 계산되던 경쟁 조건 수정 (config 로드 전 시작)
- 앱에서 언어를 전환하면 설정 입력값이 비워지고 상태가 "연결 중..."에 머무는 문제 수정 — 전환 후 설정/연결 상태를 다시 불러오고 보고 있던 탭을 유지
- 트레이 메뉴에 "종료"와 Fyne 기본 "Quit"이 함께 표시되던 문제 수정
- 트레이 아이콘 왼쪽 클릭 시 메뉴가 뜨던 동작을 창 열기로 변경 (메뉴는 우클릭)

### 업그레이드 안내 (v0.3.x → v0.3.2)
- exe 교체만으로 업그레이드 가능 (서비스 중지 → 교체 → 시작). 서비스 재설치 불필요
- 기존 config.json 그대로 호환 — 새 설정은 기본값 적용 (`webui_remote`=false, `shutdown_grace`=true)
- **동작 변화 주의**: 브라우저 WebUI가 기본 꺼짐 / SmartThings 전원 명령에 5분 유예가 기본 적용됨 (설정에서 끄기 가능)

## [v0.3.1]

### 새 기능
- 더블클릭 GUI 서비스 관리 패널 (Install/Uninstall/Start/Open WebUI)
- 서비스 상태 실시간 표시 (Running/Stopped/Not installed)
- 상태에 따라 버튼 자동 전환 (Install ↔ Uninstall)
- 로딩 상태 표시 (Installing.../Starting.../Removing...)
- GUI 에러 발생 시 gui.log에 기록

### 개선
- svc.IsWindowsService()로 서비스/인터랙티브 세션 정확 판별
- CLI 명령어 그대로 유지 (install/uninstall/status/version/run)

## [v0.3.0]

### 새 기능
- WebUI에 WoL 상태 표시 (어댑터별 MAC/IP/WoL 상태 + 외부 IP)
- WebUI에 예약 종료(스케줄) 기능 (타이머 + 카운트다운 + 취소)
- 다국어 지원 (한국어/영어, 브라우저 감지 + 수동 전환)
- 다크/라이트 모드 (시스템 감지 + 수동 전환)
- 버전 표시 (WebUI 헤더)
- Config 핫 리로드 (secret 변경 시 서비스 재시작 불필요)
- 서비스 재시작 메커니즘 개선 (sc.exe 기반, Recovery Action 의존 제거)

### 보안
- WebUI HTML 인젝션 취약점 수정 (html/template 사용)
- 로그인 rate limiting (5회 실패 시 60초 잠금)
- 로거 경쟁 조건 수정 (sync.Mutex)

### 개선
- 외부 IP 조회 캐싱 (10분 TTL)
- 포트 유효성 검증 (1-65535)
- 방화벽 규칙 에러 처리 + 사용자 안내
- WoL PowerShell 실패 시 graceful 처리 (기본 정보는 표시)
- Save 버튼 변경감지 (변경 시에만 활성화)
- 서비스 로그 자동 새로고침 기본 활성화

### 리팩토링
- HTTP 핸들러를 테스트 가능한 구조로 분리
- WebUI HTML을 embed 패키지로 분리 (html/template)
- CSS 변수 기반 디자인 시스템
- 2x2 그리드 대시보드 레이아웃
- Inter + Noto Sans KR 폰트 (Google Fonts)

## [v0.2.0]

### 보안
- WebUI에 인증(secret 기반) 및 CSRF 보호 추가
- secret 미설정 시 경고 메시지 출력
- 로그에 secret 값 마스킹 처리

### 새 기능
- 로그 로테이션 (1MB 초과 시 최대 3개 백업)
- 버전 정보 표시 (`version` 명령)
- WebUI에 Force Shutdown / Suspend 테스트 버튼 추가
- WebUI에 서비스 재시작 버튼 추가
- WebUI에 실시간 로그 뷰어 추가 (자동 새로고침, 브라우저 시간대 변환)
- WebUI 2단 레이아웃 (좌: 설정 / 우: 로그) + 반응형
- HTTP 서버 Graceful Shutdown

### 버그 수정
- 방화벽 규칙 삭제 시 포트 하드코딩 → 고정 이름으로 수정
- Install 시 기존 서비스 삭제 로직 개선 (폴링 기반 대기)
- config.json 파싱 에러 시 경고 로그 출력
- 로그 파일 핸들 Close 처리
- turnscreenoff: Session 0 격리 문제 해결 (유저 세션에서 실행)
- 명령 실행 결과 항상 로깅

### 리팩토링
- 명령 핸들링을 Commands 맵으로 통합
- 유저 세션 헬퍼 (`usersession.go`) 분리

### CI
- GitHub Actions Go 버전을 go.mod 기반으로 자동 동기화
- 빌드 시 ldflags로 버전 자동 주입
- CHANGELOG.md 기반 릴리즈 노트 자동 추출

### 문서
- README에 exe 설치 위치 권장 및 설정 보관 안내 추가
- turnscreenoff 제약사항 안내 (유저 로그인 필요)

## [v0.1.0]

- 초기 릴리즈
- SmartThings PCControl Edge 드라이버 호환 HTTP 서버
- Windows 서비스로 동작
- 지원 명령: ping, shutdown, forceshutdown, restart, hibernate, suspend, lock, turnscreenoff
- WebUI (설정 + 명령 테스트)
- 한 줄 설치 (install/uninstall/status/run)
