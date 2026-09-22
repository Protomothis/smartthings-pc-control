# SmartThings Edge 드라이버

PC Control 서비스를 위한 전용 SmartThings Edge 드라이버다. 허브 안에서 로컬로 돌면서
PC의 `/st/v1` API로 이야기한다.

- **실제 전원 상태** — 켜짐 · 절전 · 최대 절전 · 꺼짐 · 깨우는 중 · 종료 대기. 스위치가 실제 상태와 어긋나지 않는다.
- **유예와 예약이 보인다** — 남은 시간, 실행 시각, 출처(SmartThings · 앱 · 텔레그램), 취소.
- **IP를 손으로 넣지 않는다** — SSDP 자동 검색으로 주소·포트·호스트 이름이 채워진 채 장치가 생긴다. DHCP로 주소가 바뀌어도 따라간다.
- **Wake-on-LAN** — 스위치를 켜면 매직 패킷을 보내고 결과를 말해 준다.
- **조용한 실패가 없다** — 시크릿 불일치, 연결 불가, 버전 비호환, WoL 미준비를 한 줄로 알려 준다.
- **여러 PC** — Windows MachineGuid로 구분하므로 허브 하나가 여러 PC를 다뤄도 섞이지 않는다.

기존 [PCControl 드라이버](https://github.com/toddaustin07/PCControl)는 그대로 동작한다.
레거시 명령 경로(`/{secret}/{command}`)를 바꾸지 않았으므로 옮겨 갈 의무는 없다.

## 요구 사항

| | |
|---|---|
| 허브 | Edge 드라이버를 실행할 수 있는 SmartThings 허브 |
| 서비스 | PC Control **v1.1.0 이상**. 그보다 낮으면 드라이버가 `버전 불일치`로 표시한다 |
| 네트워크 | 허브와 PC가 같은 서브넷/VLAN(자동 검색과 WoL에 필요) |
| 방화벽 | 인바운드 TCP 5001, 자동 검색을 쓰면 인바운드 **UDP 1900**. 둘 다 `install`이 만든다 |
| 시크릿 | 필수는 아니지만 권장. 비어 있으면 LAN의 누구나 PC를 제어할 수 있고 드라이버가 경고한다 |

네트워크 프로필은 **개인(Private)** 이어야 한다. 공용에서는 규칙과 무관하게 Windows가
인바운드 멀티캐스트를 막는다. 라우터는 `239.255.255.250`을 구간 사이로 전달하지 않으므로
서브넷이 다르면 자동 검색 대신 수동 추가를 쓴다(명령과 푸시는 그대로 동작한다).

## 설치

### 1. 채널 가입

> **채널 초대 링크:** `(채널 생성 후 이 자리에 링크를 넣는다)`

링크를 열고 → [Enroll] → 허브를 선택한다.

### 2. 드라이버 설치

가입한 채널의 **SmartThings PC Control**을 [Install] 한다. 설치된 드라이버는 앱의
*메뉴 → 설정 → 연결된 서비스 → 허브 → 드라이버*에서 확인할 수 있다.

### 3. PC 준비

1. 데스크톱 앱 **설정 탭**에서 시크릿을 정하고 [저장].
2. **네트워크 탭 → SmartThings**에서 *자동 검색(SSDP) 허용*이 켜져 있는지 본다(기본 켬).
3. WoL로 깨울 계획이면 같은 탭에서 어댑터의 WoL 상태를 확인한다.

### 4. 장치 추가 — 자동

SmartThings 앱에서 **[+] → 기기 추가 → 주변 기기 검색**. 드라이버가 LAN에 M-SEARCH를
보내고 응답한 PC마다 장치를 만든다. **IP·포트·호스트 이름이 채워진 채** 생기므로 장치
설정에서 **시크릿만** 넣으면 끝이다.

### 5. 장치 추가 — 수동

검색이 아무것도 찾지 못하면 같은 [주변 기기 검색]이 **PC Control (set IP in settings)**
장치를 하나 만든다. 장치 설정에서 **IP 주소**와 **시크릿**을 넣는다.

빈 장치가 쌓이지는 않는다. IP가 비어 있는 장치가 하나라도 있으면 다시 검색해도 새로
만들지 않는다. 수동으로 추가한 장치도 첫 상태 조회에 성공하면 PC의 식별자를 기억하므로,
나중에 자동 검색이 같은 PC를 찾아도 중복으로 만들지 않고 주소만 갱신한다.

## 화면

**대시보드** — 스위치와 전원 상태.

**상세 화면** — 상태 카드가 위, 조작 카드가 아래다.

| 상태 카드 | 보여 주는 것 |
|---|---|
| 전원 상태 | 켜짐 · 절전 · 최대 절전 · 꺼짐 · 깨우는 중 · 종료 대기 |
| 마지막 실행 | `종료 · SmartThings · 23:05`. 아직 없으면 `없음 (None)` |
| 예약 요약 | `종료 · 4분 후`. 한 시간이 넘으면 `종료 · 2시간 후` · `종료 · 1일 3시간 후`. 없으면 `없음` |
| 세션 | `잠김 · 유휴 20분 · kim`. 노출을 끄면 `세션 정보 꺼짐` |
| 상태 | `연결됨 · v1.1.0`, 또는 `연결 안 됨 · 시크릿 불일치`처럼 이유까지 |
| 버전 | `v1.1.0 · 드라이버 1.0`. 업데이트가 있으면 ` · 업데이트 v1.2.0` |

| 조작 카드 | 하는 일 |
|---|---|
| 명령 | 깨우기 · 절전 · 최대 절전 · 재시작 · 종료 · 잠금 · 화면 끄기 · 화면 켜기 |
| 예약할 명령 | 시간만 고르는 예약이 무엇을 실행할지: 종료 · 재시작 · 절전 · 최대 절전 |
| 예약 시간 | 5 · 10 · 15 · 30 · 45분, 1 · 1.5 · 2 · 3 · 4 · 6 · 8 · 12시간, 1 · 2 · 3일, 그리고 **취소**. 목록 자체는 `시간 선택…`에 머문다 |

**프로필 이름이 중요한 이유**: 장치의 화면은 **추가한 시점의 정의로 굳는다.** "왜 아직
옛날 화면이지?"의 답은 대개 장치가 아직 옛 프로필(`pc.vN`)에 있다는 것이다. 드라이버가
첫 `init`에서 현재 프로필(`pc.v15`)로 옮긴다.

## 사용

### 스위치

- **켜기** → 매직 패킷을 즉시 · 2초 뒤 · 5초 뒤 세 번, 포트 7과 9 양쪽으로 보낸다. 상태가 `깨우는 중`이 되고, PC가 응답하면 `켜짐`이 된다. 90초 안에 응답이 없으면 이전 상태로 돌아가고 "깨우기 실패"를 표시한다.
- **끄기** → 환경설정 `스위치 끄기 동작`의 명령을 보낸다. 기본은 종료이고, PC에 유예가 걸려 있으면 그 유예를 따른다.

스위치는 전원 상태가 `켜짐` · `깨우는 중` · `종료 대기`일 때 켜짐으로 보인다. **PC에서
유예를 취소하면 스위치가 다시 켜짐으로 돌아온다.**

### 명령

명령 목록에서 하나를 고르면 바로 나간다. 유예를 따를지는 `버튼 실행 방식` 환경설정이
정한다(기본은 PC에 설정된 유예를 따름). 무엇이 실행됐는지는 **마지막 실행** 줄이 말해
준다. 목록 자체는 언제나 `명령 선택…`에 머문다 — 목록을 고르지 않고 닫으면 앱이 그 줄의
현재 값을 그대로 보내기 때문이다.

화면 켜기·끄기는 **로그인된 세션이 있어야** 동작한다(잠금 화면에서도 된다).

### 예약

**예약할 명령**에서 무엇을 예약할지 고르고, **예약 시간**에서 얼마 뒤인지 고른다.
5분부터 **3일(72시간)** 까지 고를 수 있다(#89).
예약은 PC당 하나이고 새 예약이 기존 것을 대체한다. 예약 시간 목록의 **취소**가 예약을
지운다. 앱·트레이 토스트·텔레그램 어디서 취소해도 즉시 서로 반영된다.

예약 시간 목록도 명령 목록과 같이 언제나 `시간 선택…`에 머문다. 고르지 않고 닫으면 앱이
그 줄의 현재 값을 보내는데, 그 값이 아무것도 하지 않는 값이어야 하기 때문이다.

### 자동화 예시

**자정 취침 예약** — 잊고 켜 둔 PC를 30분 뒤 종료하되 취소할 여지를 둔다.

```
조건(If)  : 시각이 00:00 이고 PC 의 전원 상태가 켜짐
동작(Then): PC 의 pcDelay.schedule (minutes: 30, command: shutdown)
```

**외출하면 잠그기**

```
조건(If)  : 구성원 전원이 집을 떠남
동작(Then): PC 의 pcExec.execute (command: lock, mode: immediate)
```

**책상 주변기기 전원 연동**

```
조건(If)  : PC 의 전원 상태가 깨우는 중 또는 켜짐 으로 바뀜
동작(Then): 책상 플러그 켜기

조건(If)  : PC 의 전원 상태가 꺼짐 또는 절전 으로 바뀜
동작(Then): 책상 플러그 끄기
```

`깨우는 중`을 조건에 넣으면 WoL로 깨우는 동안 모니터가 미리 켜져 부팅 화면을 놓치지 않는다.

그 밖에 조건으로 쓸 수 있는 것: 예약 상태·예약 여부·예약할 명령, 연결 상태
(`응답 없음`이면 PC나 네트워크 이상을 알림으로 받을 수 있다), 잠금 여부(세션 노출을 켠 경우).

## 환경설정

장치 화면 오른쪽 위 **⋮ → 설정**.

| 설정 | 설명 | 기본값 |
|---|---|---|
| PC IP 주소 | PC의 IPv4. **비워 두면** 자동 검색으로 알아낸 주소를 쓴다. 채워 넣으면 언제나 이 값이 이긴다 | `""` |
| 검색 따라가기 | 자동 검색이 같은 PC를 다른 IP로 알려 오면 따라간다. 끄면 지금 주소에 고정 | 켬 |
| 서비스 포트 | 서비스의 명령 포트 | `5001` |
| 시크릿 | 설정 탭의 시크릿. `X-PC-Secret` 헤더로 보낸다. **Edge에는 비밀번호 입력 타입이 없어 입력하는 동안 화면에 그대로 보인다** | `""` |
| MAC 주소 | WoL용 MAC. 비워 두면 서비스가 보고한 WoL 가능 어댑터의 MAC을 쓴다 | `""` |
| WoL 브로드캐스트 | 매직 패킷을 보낼 주소. 공유기가 `255.255.255.255`를 막으면 서브넷 브로드캐스트(예: `192.168.1.255`) | `255.255.255.255` |
| 상태 확인 주기 | 10초 / 30초 / 1분 / 5분 | 30초 |
| 스위치 끄기 동작 | 스위치를 끌 때 보낼 명령 | 종료 |
| 버튼 실행 방식 | 명령 목록이 PC의 유예를 따를지, 즉시 실행할지. 자동화의 `execute`는 자기 `mode`를 따로 가진다 | 설정된 유예 따름 |
| 문구 언어 | 상태·예약 문구의 언어. 드라이버는 허브 로케일을 읽을 수 없어 자동은 한국어다 | 자동 (한국어) |

푸시 구독에 성공해도 폴링 주기는 사용자가 정한 값을 유지한다. 푸시가 즉시 반영을
담당하고 폴링은 안전망이다.

**서비스 쪽 설정**은 데스크톱 앱 **네트워크 탭 → SmartThings**에 있다: 연결된 허브,
*자동 검색(SSDP) 허용*, *세션 정보 노출(잠금·유휴)*, *사용자 이름 포함*, 허브 허용 목록.

## 여러 PC

- 장치 하나가 PC 하나다. 드라이버는 IP가 아니라 **Windows MachineGuid**로 PC를 구분한다.
- 시크릿·MAC·브로드캐스트·포트는 모두 **장치별** 설정이다.
- 푸시는 드라이버당 리스너 하나로 모든 PC의 이벤트를 받아 식별자로 나눠 준다.
- 장치가 여럿이면 폴링 시각을 흩어 같은 초에 모든 PC를 찌르지 않는다.
- 디스크 이미지로 복제한 PC는 MachineGuid가 같아 **장치 하나로 합쳐진다.** 드라이버가 "같은 식별자인데 호스트 이름이 다르다"를 감지해 경고하므로, 한쪽에서 Sysprep을 돌리거나 레지스트리의 `MachineGuid`를 새로 만든다.

## 문제 해결

| 증상 | 확인할 것 |
|---|---|
| **명령을 보내면 회전 표시 뒤 오류** | 드라이버가 오래됐다. 값이 바뀌지 않는 명령(예약 없을 때 취소, 같은 값 재선택)의 응답을 강제로 내보내는 수정이 1.0.0에 들어 있다. 채널에서 드라이버를 업데이트하고, 화면이 그대로면 **버전 줄의 `화면 pc.vN`**을 확인한다 |
| **줄에 "-"만 보이거나 "상태를 모두 보고하지 않았습니다"** | 프로필 이전 직후 한 번 나타날 수 있다. [새로 고침]을 누르거나 다음 폴링을 기다린다. 계속되면 장치를 지우고 다시 추가한다 |
| **검색해도 아무것도 안 나옴** | 네트워크 탭에서 *자동 검색(SSDP) 허용*이 켜져 있는지. UDP 1900 방화벽 규칙과 네트워크 프로필(개인). 허브와 PC가 같은 서브넷인지. Wi-Fi의 AP·클라이언트 격리. 안 되면 수동 추가로 넘어간다 |
| **`시크릿 불일치`** | 장치 설정의 시크릿과 PC 설정 탭의 시크릿이 같은지. 메시지가 "허브가 허용 목록에 없습니다"라면 시크릿이 아니라 네트워크 탭의 **허용 목록**이 문제다 — [현재 허브 추가]를 누르거나 목록을 비운다 |
| **`응답 없음`** | PC가 켜져 있고 서비스가 도는지(`smartthings-pc-control.exe status`). IP·포트가 맞는지. TCP 5001 방화벽. IP가 바뀌었다면 `PC IP 주소`를 비우고 `검색 따라가기`를 켠다 |
| **`버전 불일치`** | 서비스가 v1.1.0 미만이다. 반대로 "드라이버 업데이트 필요"면 채널에서 드라이버를 올린다 |
| **스위치 켜기가 안 먹음** | 상태 줄이 WoL 미준비를 말하면 어댑터의 Wake-on-LAN이 꺼져 있다(장치 관리자 → 어댑터 → 전원 관리). 공유기가 `255.255.255.255`를 막으면 `WoL 브로드캐스트`를 서브넷 브로드캐스트로 바꾼다. 절전이 아니라 **완전 종료**에서 깨우려면 메인보드의 "PCIE로 깨우기"와 **빠른 시작 해제**도 필요하다 |
| **상태가 늦게 갱신됨** | 푸시 구독이 실패하고 폴링만 도는 상태일 수 있다. 서비스 로그에 subscribe `400`이 남았는지 보고, 폴링 주기를 줄여 본다 |
| **장치가 두 개로 보임** | 수동 추가 뒤 자동 검색이 같은 PC를 다시 찾은 경우다. 시크릿을 넣어 연결을 성공시키면 식별자를 학습하므로, 그 뒤 남는 쪽을 지운다 |
| **커스텀 줄이 하나도 안 보임** | 커스텀 capability가 계정에 만들어지지 않았다. 이 경우에도 스위치와 새로 고침은 동작한다 |

로그:

- 서비스 — exe 옆 `service.log`, 또는 데스크톱 앱의 **로그 탭**
- 드라이버 — `smartthings edge:drivers:logcat <driverId> --hub-address <허브IP>`

## 개발

```
edge/
  config.yml              드라이버 메타데이터, permissions(lan, discovery)
  src/                    Lua 모듈 (설계: ../docs/design/edge-driver.md)
  profiles/               pc-vN.yml — 현재는 pc-v15.yml
  capabilities/           커스텀 capability 정의·프레젠테이션·번역(ko/en)
  tests/                  fengari로 도는 Lua 5.3 테스트
  tools/                  테스트 러너와 배포 스크립트
```

### 테스트

```bash
cd edge
npm install                       # fengari 하나뿐
npm test                          # tests/run.lua
node tools/lua.js tests/syntax.lua  # src/ 전 모듈 컴파일
```

로컬에서 bun을 쓰면 `bun tools/lua.js tests/run.lua`로 같은 것이 돈다.
`capabilities_test.lua`가 정의·프레젠테이션·드라이버의 정합성을 지키므로, 화면을 바꾸면
여기가 먼저 알려 준다.

### 네임스페이스

capability id는 `<네임스페이스>.<이름>`이고, 네임스페이스는 SmartThings가 계정에
발급한다. 다른 계정으로 옮기려면:

```bash
node tools/apply-namespace.js <네임스페이스>   # caps.lua · 프로필 · capabilities/*.json 일괄
npm test
```

### capability 업로드

```bash
./tools/create-capabilities.sh    # 계정에 한 번만: 여섯 capability와 프레젠테이션 생성
./tools/sync-capabilities.sh      # 이후 변경분 반영(정의·프레젠테이션·번역)
./tools/sync-capabilities.sh --dry-run
```

`smartthings` CLI가 필요하다(`npm i -g @smartthings/cli`). CLI 2.x에는 `login` 명령이
없고, 인증이 필요한 첫 명령에서 브라우저가 열린다.

`sync-capabilities.sh`는 **이미 존재하는** id만 갱신한다. 정의(속성·명령)를 바꿔야 하면
새 id로 만들어야 한다 — 허브가 capability 정의를 id 단위로 캐시하고 바뀐 정의를 다시
읽지 않기 때문이다. 자세한 규칙은
[`../docs/design/edge-platform-notes.md`](../docs/design/edge-platform-notes.md)에 있다.

### 프로필 버전 규칙

장치의 화면은 **생성 시점의 프레젠테이션으로 굳는다.** 같은 이름의 프로필을 다시
패키징하면 preference 변경만 반영되고 화면은 그대로다. 그래서:

1. 프레젠테이션이나 capability 목록을 바꾸면 `profiles/pc-vN.yml`(`name: pc.vN`)을 새로 만든다.
2. 옛 프로필 파일은 **패키지에 남긴다.** 아직 옮겨지지 않은 장치가 참조한다.
3. `src/profiles.lua`의 `PC`와 `KNOWN`만 고치면 `init`/`added`가 기존 장치를 옮긴다.
4. capability id가 바뀌었다면 `poll.ROWS_VERSION`도 올린다. 새 id의 속성은 허브에서 값 없이 시작하므로 한 번 다시 칠해야 한다.

### 패키징

```bash
smartthings edge:drivers:package .
smartthings edge:channels:assign <driverId> <version> --channel <channelId>
```

CI가 `edge-vX.Y.Z` 태그에서 같은 일을 한다. 태그는 `src/driver_version.lua`와 일치해야
하며, 다르면 워크플로가 실패한다.

---

## English summary

A purpose-built SmartThings Edge driver for the PC Control service. It runs locally on the
hub and talks to the PC's `/st/v1` API (service **v1.1.0 or newer**).

- Real power state — on, sleeping, hibernated, off, waking, shutting down. The switch is
  derived from it, so it never sticks.
- Grace periods and schedules are visible: remaining time, execute time, origin, cancel.
- SSDP discovery fills in address, port and hostname — only the secret has to be typed.
- Wake-on-LAN with retries, and a plain-language reason when it cannot work.
- Failures are named: secret mismatch, unreachable, incompatible version, WoL not ready.
- One device per PC, keyed by Windows MachineGuid, so one hub can drive several PCs.

**Install** — enroll in the channel (link above), install the driver, then *Add device →
Scan nearby*. Fill in the secret in the device settings. If discovery finds nothing, the
same scan creates a device for manual setup; enter the IP and the secret.

**Screen** — a status card (power state, last action, schedule summary, session, status,
versions) and a control card (command list, what to schedule, when to schedule).
Preference labels are Korean with the English term in parentheses; Edge has no per-locale
preference variants.

**Develop** — `npm test` runs the Lua 5.3 suite under fengari. `tools/apply-namespace.js`
rewrites the capability namespace, `tools/sync-capabilities.sh` uploads definitions,
presentations and translations. A presentation change needs a new profile name
(`profiles/pc-vN.yml`); a definition change needs a new capability id. The design contract
is in [`../docs/design/edge-driver.md`](../docs/design/edge-driver.md) and the measured
platform behaviour in
[`../docs/design/edge-platform-notes.md`](../docs/design/edge-platform-notes.md).
