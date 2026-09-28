<div align="center">

# SmartThings PC Control

**Windows PC 전원을 SmartThings와 텔레그램으로 제어하는 Windows 서비스 + 트레이 앱**<br>
<sub>Windows PC power control for SmartThings and Telegram: shutdown, restart, sleep, hibernate, lock, Wake-on-LAN, scheduling</sub>

[![Release](https://img.shields.io/github/v/release/Protomothis/smartthings-pc-control?style=flat-square)](https://github.com/Protomothis/smartthings-pc-control/releases)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go)](https://go.dev)
[![License](https://img.shields.io/github/license/Protomothis/smartthings-pc-control?style=flat-square)](LICENSE)
[![Windows](https://img.shields.io/badge/Windows-10%20%7C%2011-0078D6?style=flat-square&logo=windows)](https://www.microsoft.com/windows)

[한국어](#한국어) · [English](#english)

<img src="docs/gui-settings.png" alt="설정 탭" width="49%"> <img src="docs/gui-logs.png" alt="로그 탭" width="49%">

</div>

---

## 한국어

단일 exe 하나가 Windows 서비스(로그인 없이 항상 실행)와 트레이 앱을 겸하고, 전용 SmartThings Edge 드라이버가 허브 안에서 로컬로 이 서비스와 이야기합니다.

### 무엇을 할 수 있나

- **전원 명령** — 종료 · 재시작 · 절전 · 최대 절전 · 잠금 · 화면 끄기/켜기 · 강제 종료. 원격 전원 명령은 유예(기본 5분) 뒤 실행되고 어디서든 취소할 수 있습니다.
- **실제 전원 상태** — SmartThings 앱에 켜짐 · 절전 · 최대 절전 · 꺼짐 · 깨우는 중 · 종료 대기가 그대로 보이고, 서비스가 종료·절전 직전에 허브로 푸시합니다.
- **Wake-on-LAN** — 스위치를 켜면 PC가 고른 어댑터의 MAC으로 매직 패킷을 보냅니다.
- **예약** — 5분부터 3일까지 프리셋 16개, 취소 포함. SmartThings · 앱 · 텔레그램 어디서 건 예약이든 모든 곳에 같이 보입니다.
- **전환 중 보호** — PC가 꺼지거나 켜지는 동안에는 명령 목록이 `종료 진행 중…`처럼 바뀌고 명령을 보내지 않습니다.
- **텔레그램** — 봇으로 알림을 받고 `/status` `/shutdown 30` 같은 명령으로 제어합니다(선택).
- **데스크톱 앱** — 설정 · 명령 · 예약 · 알림 · 네트워크 · 로그 탭, 트레이 상주, 한국어/English.
- **서명된 자동 업데이트** — Ed25519 서명 매니페스트로 검증한 릴리스만 설치하고, 실패하면 롤백합니다.

### 설치

**1. Windows 앱 설치와 설정**

1. [Releases](https://github.com/Protomothis/smartthings-pc-control/releases)에서 `smartthings-pc-control.exe`를 받아 고정된 폴더에 둡니다(권장 `C:\Program Files\SmartThings PC Control\`). 설정 `config.json`과 로그 `service.log`가 exe 옆에 생기므로 설치 뒤에는 exe를 옮기지 마세요.
2. exe를 실행하고 **설정 탭 → [설치]** → UAC 승인. CLI로는 관리자 권한에서 `smartthings-pc-control.exe install`입니다. 서비스 등록, 자동 시작, 방화벽 인바운드 규칙(TCP 5001, UDP 1900)이 함께 만들어집니다.
3. 설정 탭에서 **시크릿**을 정하고 [저장]합니다.

**2. 채널 가입과 드라이버 설치**

1. 채널 **Protomothis** 초대 링크를 열고 [Enroll] → 허브 선택: <https://bestow-regional.api.smartthings.com/invite/Kr2zNWYgpp2A>
2. 채널의 **SmartThings PC Control** 드라이버를 [Install]합니다.

**3. SmartThings 앱에서 [주변 기기 검색]**

1. **PC가 켜져 있고 PC Control이 돌고 있는 상태에서** SmartThings 앱 **[+] → 기기 추가 → 주변 기기 검색**을 누릅니다. 장치를 추가하는 방법은 이 검색뿐입니다.
2. IP · 포트 · 호스트 이름이 채워진 채 장치가 생기므로, 장치 설정에 **시크릿만** 넣으면 끝입니다.
3. 장치 정보의 모델 `PC Control · <id 8자리>`는 앱 네트워크 탭의 **이 PC의 ID**와 같은 값이라, PC가 여럿일 때 어느 장치가 어느 PC인지 여기서 맞춰 봅니다.

드라이버는 서비스 **v1.1.0 이상**이 필요하고, **v1.1.1**을 권장합니다(네트워크 탭의 검색 상태 표시와 WoL 어댑터 선택). 허브와 PC는 같은 서브넷에 있어야 하고 네트워크 프로필은 **개인**이어야 합니다. 검색이 안 되면 앱 **네트워크 탭 → SmartThings**의 검색 상태와 마지막 검색 요청 시각부터 확인하세요.

### 텔레그램 (선택)

[@BotFather](https://t.me/BotFather)로 봇을 만들고, 앱 **알림 탭**에 토큰을 넣은 뒤 [Chat ID 찾기]로 채팅을 고릅니다. 알림과 명령 제어는 각각 따로 켭니다. 포트 개방이나 웹훅은 필요 없고, **봇 하나에 PC 하나**를 씁니다(같은 토큰을 공유하면 한 PC만 명령을 받습니다).

### 문서

자세한 안내는 [Wiki](https://github.com/Protomothis/smartthings-pc-control/wiki)에 있습니다.

- [설치와 첫 설정](https://github.com/Protomothis/smartthings-pc-control/wiki/설치와-첫-설정)
- [SmartThings Edge 드라이버](https://github.com/Protomothis/smartthings-pc-control/wiki/SmartThings-Edge-드라이버) · 개발자용 [`edge/README.md`](edge/README.md)
- [텔레그램](https://github.com/Protomothis/smartthings-pc-control/wiki/텔레그램)
- [문제 해결과 FAQ](https://github.com/Protomothis/smartthings-pc-control/wiki/문제-해결과-FAQ)
- 참고: [설정 파일 레퍼런스](https://github.com/Protomothis/smartthings-pc-control/wiki/설정-파일-레퍼런스) · [CLI와 API 레퍼런스](https://github.com/Protomothis/smartthings-pc-control/wiki/CLI와-API-레퍼런스) · [CHANGELOG](CHANGELOG.md) · [edge/CHANGELOG](edge/CHANGELOG.md)

### 개발

Fyne GUI 때문에 CGO와 MinGW-w64 gcc가 필요합니다.

```bash
CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go build -ldflags="-s -w -H=windowsgui -X main.Version=dev" -o smartthings-pc-control.exe .
cd edge && npm install && npm test      # Lua 5.3 테스트를 fengari로 실행
```

CI는 push와 PR마다 vet · test · 빌드(`ci.yml`)와 `edge/**` 변경의 Lua 테스트(`edge.yml`)를 돌립니다. `v*` 태그는 앱을 빌드·서명해 릴리스하고(`release.yml`), `edge-vX.Y.Z` 태그는 드라이버를 패키징해 채널에 배정합니다. 드라이버 설계는 [`docs/design/edge-driver.md`](docs/design/edge-driver.md)에 있습니다.

### 보안

SmartThings 명령은 포트 5001에서 받고, 드라이버는 시크릿을 URL이 아니라 `X-PC-Secret` 헤더로 보냅니다. 시크릿이 비어 있으면 LAN의 누구나 PC를 제어할 수 있으니 꼭 정하세요. `smartthings.allowed_hubs`에 허브 IP를 넣으면 그 허브만 드라이버 API를 쓸 수 있습니다(비어 있으면 모두 허용). 앱이 쓰는 로컬 API는 `127.0.0.1:5002`에만 열리고, 브라우저 WebUI는 시크릿을 정한 뒤 직접 켰을 때만 LAN에 열립니다. 텔레그램 봇 토큰은 DPAPI로 암호화해 저장합니다. 자동 업데이트는 내장 공개키로 Ed25519 매니페스트 서명과 exe의 SHA-256을 확인한 뒤에만 설치합니다. 예전 형식의 `/{secret}/{command}` 경로도 호환을 위해 남아 있습니다.

지원 환경: Windows 10 · 11(데스크톱 앱은 OpenGL 2.0 필요).

---

## English

A single exe that runs as a Windows service (always on, no login needed) and a tray app, plus a dedicated SmartThings Edge driver that talks to it locally from the hub.

### What it does

- **Power commands** — shutdown, restart, sleep, hibernate, lock, screen off/on, force shutdown. Remote power commands wait out a grace period (5 min by default) and can be cancelled anywhere.
- **Real power state** in the SmartThings app: on, sleeping, hibernated, off, waking, shutting down, pushed to the hub right before the PC goes down.
- **Wake-on-LAN** to the adapter the PC picks, **scheduling** with 16 presets up to 3 days (with cancel), and a command list that knows when the PC is mid-transition.
- **Telegram** notifications and bot commands (optional), a **desktop app** in Korean/English, and **signed auto-update** (Ed25519 manifest, rollback on failure).

### Install

1. **Windows app** — download `smartthings-pc-control.exe` from [Releases](https://github.com/Protomothis/smartthings-pc-control/releases) into a permanent folder (`config.json` lives next to it). Run it → **Settings → [Install]** (or `smartthings-pc-control.exe install` as admin). This creates the service and firewall rules for TCP 5001 and UDP 1900. Set a **secret** and save.
2. **Channel and driver** — open the **Protomothis** channel invite <https://bestow-regional.api.smartthings.com/invite/Kr2zNWYgpp2A>, enroll your hub, and install **SmartThings PC Control**.
3. **Scan nearby** — with the PC on and PC Control running, tap **[+] → Add device → Scan nearby** in the SmartThings app. This is the only way to add the device. Enter the secret in the device settings. The device model `PC Control · <8-char id>` matches **This PC's ID** on the app's Network tab.

The driver needs service **v1.1.0+**; **v1.1.1** is recommended (discovery status and WoL adapter choice on the Network tab). Hub and PC must share a subnet on a **Private** network profile.

### Telegram (optional)

Create a bot with [@BotFather](https://t.me/BotFather), paste the token on the **Notifications** tab and use [Find Chat ID]. Notifications and control are enabled separately. No open ports or webhooks; use **one bot per PC**.

### Docs

The [Wiki](https://github.com/Protomothis/smartthings-pc-control/wiki) has the guides ([English overview](https://github.com/Protomothis/smartthings-pc-control/wiki/Home-EN); most pages are Korean): [설치와 첫 설정](https://github.com/Protomothis/smartthings-pc-control/wiki/설치와-첫-설정) (install), [SmartThings Edge 드라이버](https://github.com/Protomothis/smartthings-pc-control/wiki/SmartThings-Edge-드라이버) (Edge driver), [텔레그램 알림 설정](https://github.com/Protomothis/smartthings-pc-control/wiki/텔레그램) (Telegram), [문제 해결과 FAQ](https://github.com/Protomothis/smartthings-pc-control/wiki/문제-해결과-FAQ) (troubleshooting). Driver developers: [`edge/README.md`](edge/README.md).

### Development

CGO and MinGW-w64 gcc are required (Fyne GUI). `go vet ./... && go test ./...` and the `go build` line above; `cd edge && npm install && npm test` runs the Lua 5.3 suite under fengari. CI runs both on every push and PR; a `v*` tag builds, signs and releases the app, and an `edge-vX.Y.Z` tag packages the driver and assigns it to the channel.

### Security

The driver sends the secret in the `X-PC-Secret` header, never in the URL; an empty secret lets anyone on the LAN control the PC. `smartthings.allowed_hubs` restricts the driver API to listed hub IPs (empty allows any). The app's local API binds to `127.0.0.1:5002`; the browser WebUI opens to the LAN only when you enable it with a secret set. The bot token is DPAPI-encrypted, and updates install only after the Ed25519 manifest signature and the exe's SHA-256 check out.

---

## License

[MIT](LICENSE)
