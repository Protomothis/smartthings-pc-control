# Changelog

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
