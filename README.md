# SmartThings PC Control

SmartThings Edge 드라이버 호환 Windows PC 전원 제어 서비스입니다.  
[Remote Shutdown Manager (Karpach)](https://github.com/karpach/remote-shutdown-pc)의 대체품으로, [PCControl Edge 드라이버](https://github.com/toddaustin07/PCControl)와 100% 호환됩니다.

## 왜 만들었나요?

기존 Remote Shutdown Manager는:
- 유저 로그인 + 데스크톱 세션이 필수 (시스템 트레이에서 동작)
- .NET Framework 4.8 런타임 필요
- 유저가 로그아웃하면 동작 중지

이 프로젝트는:
- **Windows 서비스**로 동작 → 유저 로그인 불필요
- **단일 exe** → 런타임 설치 없음
- **한 줄 설치** → `install` 명령 하나로 끝

## 지원 명령어

| 명령 | 동작 |
|------|------|
| `ping` | 상태 확인 (200 OK 응답) |
| `shutdown` | 종료 (5초 대기) |
| `forceshutdown` | 즉시 강제 종료 |
| `restart` | 재시작 (5초 대기) |
| `hibernate` | 최대 절전 모드 |
| `suspend` | 절전 모드 (슬립) |
| `lock` | 모든 활성 세션 잠금 |
| `turnscreenoff` | 모니터 끄기 |

## 설치

1. [Releases](https://github.com/Protomothis/smartthings-pc-control/releases) 페이지에서 `smartthings-pc-control.exe` 다운로드
2. 관리자 권한으로 실행:

```
smartthings-pc-control.exe install
```

끝! 서비스가 자동으로 등록되고, 방화벽 규칙이 추가되며, 부팅 시 자동 시작됩니다.

## 사용법

```
smartthings-pc-control.exe install     # 서비스 설치 + 시작
smartthings-pc-control.exe uninstall   # 서비스 제거
smartthings-pc-control.exe status      # 상태 확인
smartthings-pc-control.exe run         # 콘솔 모드 (디버그)
```

## Web UI

설치 후 브라우저에서 접속: http://127.0.0.1:5002

- 포트, 시크릿 키 설정
- 명령어 테스트 버튼

## 설정

`config.json`이 exe와 같은 폴더에 생성됩니다:

```json
{
  "port": 5001,
  "secret": ""
}
```

- `port`: SmartThings Hub가 요청을 보내는 포트 (기본값: 5001)
- `secret`: 인증 키 (비어있으면 인증 없이 동작)

## SmartThings 설정

1. SmartThings Hub에 [PCControl Edge 드라이버](https://github.com/toddaustin07/PCControl) 설치
2. 디바이스 설정에서 PC의 IP 주소 입력
3. 포트와 시크릿을 이 서비스와 동일하게 설정

기존에 Remote Shutdown Manager를 사용하고 있었다면 **SmartThings 쪽은 아무것도 바꿀 필요 없습니다.**

## 빌드

```bash
go build -ldflags="-s -w" -o smartthings-pc-control.exe .
```

## 버전 관리

이 프로젝트는 [Semantic Versioning](https://semver.org/)을 따릅니다.

- `v0.x.x` — 초기 개발 단계
- `v1.0.0` — 안정 릴리즈 (모든 기능 테스트 완료 시)

---

# English

Windows service for SmartThings PC power control.  
Drop-in replacement for [Remote Shutdown Manager (Karpach)](https://github.com/karpach/remote-shutdown-pc), fully compatible with the [PCControl Edge driver](https://github.com/toddaustin07/PCControl).

## Why?

The original Remote Shutdown Manager:
- Requires user login + desktop session (runs in system tray)
- Requires .NET Framework 4.8 runtime
- Stops working when user logs out

This project:
- **Runs as a Windows service** → no user login required
- **Single executable** → no runtime dependencies
- **One-command install** → just run `install`

## Supported Commands

| Command | Action |
|---------|--------|
| `ping` | Health check (returns 200 OK) |
| `shutdown` | Graceful shutdown (5 sec delay) |
| `forceshutdown` | Immediate forced shutdown |
| `restart` | Restart (5 sec delay) |
| `hibernate` | Hibernate |
| `suspend` | Suspend (sleep) |
| `lock` | Lock all active sessions |
| `turnscreenoff` | Turn off monitor |

## Installation

1. Download `smartthings-pc-control.exe` from [Releases](https://github.com/Protomothis/smartthings-pc-control/releases)
2. Run as administrator:

```
smartthings-pc-control.exe install
```

That's it! Service is registered, firewall rule added, and auto-start on boot is configured.

## Usage

```
smartthings-pc-control.exe install     # Install and start service
smartthings-pc-control.exe uninstall   # Remove service
smartthings-pc-control.exe status      # Show status
smartthings-pc-control.exe run         # Console mode (debug)
```

## Web UI

After installation: http://127.0.0.1:5002

- Configure port and secret key
- Test commands from browser

## Configuration

`config.json` is created next to the executable:

```json
{
  "port": 5001,
  "secret": ""
}
```

- `port`: Port the SmartThings Hub sends requests to (default: 5001)
- `secret`: Authentication key (empty = no auth)

## SmartThings Setup

1. Install the [PCControl Edge driver](https://github.com/toddaustin07/PCControl) on your SmartThings Hub
2. Set your PC's IP address in device settings
3. Match port and secret with this service

If you were already using Remote Shutdown Manager, **no changes needed on the SmartThings side.**

## Building

```bash
go build -ldflags="-s -w" -o smartthings-pc-control.exe .
```

## Versioning

This project follows [Semantic Versioning](https://semver.org/).

- `v0.x.x` — Early development
- `v1.0.0` — Stable release (after all features are tested)

## License

MIT
