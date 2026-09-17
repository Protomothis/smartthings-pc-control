# SmartThings Edge 드라이버

PC Control 서비스를 위한 **SmartThings Edge 드라이버**(Lua 5.3)입니다. 서비스의
`/st/v1` 프로토콜로 통신하며, 그 계약은
[`docs/design/edge-driver.md`](../docs/design/edge-driver.md)에 있습니다 — 코드와
문서가 어긋나면 문서를 먼저 고칩니다.

기존 [PCControl 드라이버](https://github.com/toddaustin07/PCControl)가 스위치 하나와
ping만 제공하는 것과 달리, 이 드라이버는 서비스가 이미 알고 있는 것을 전부 SmartThings로
끌어올립니다: 정확한 전원 상태(절전·최대절전·깨우는 중·종료 대기 구분), 유예
카운트다운과 출처, 예약·취소, 연결·버전·WoL 진단, 디스플레이 자식 장치, SSDP 자동 검색.

> PCControl 호환 경로(`/{secret}/{command}`)는 그대로 살아 있습니다. **기존 PCControl
> 드라이버 사용자는 아무것도 바꾸지 않아도 됩니다.** 이 드라이버는 선택지이지 교체
> 의무가 아닙니다.

---

## 요구 사항

| | |
|---|---|
| **허브** | SmartThings 허브 (Edge 드라이버를 실행할 수 있는 모델). 드라이버는 허브 안에서 로컬로 동작하며 클라우드를 거치지 않습니다 |
| **서비스** | PC Control **v1.1.0 이상**. `/st/v1` API는 v1.1.0에서 추가되었습니다. 이전 버전에 연결하면 드라이버가 `connection=incompatible`과 "서비스 v1.1.0 이상 필요" 메시지를 표시합니다 |
| **네트워크** | 허브와 PC가 **같은 서브넷/VLAN**. 드라이버는 LAN으로만 통신합니다 |
| **시크릿** | 필수는 아니지만 강력히 권장합니다. 비어 있으면 LAN의 누구나 PC를 제어할 수 있고, 드라이버는 `pcStatus.message`에 경고를 띄웁니다 |

### 방화벽과 네트워크

TCP 명령 포트(기본 5001)는 서비스 설치 시 열립니다. **SSDP 자동 검색**은 멀티캐스트라
추가 조건이 있습니다.

- **UDP 1900 인바운드**가 PC에서 허용되어야 합니다. `pc-control.exe install`이
  *SmartThings PC Control SSDP* 규칙을 추가하고, `smartthings.discovery`가 켜져 있는 한
  서비스가 시작할 때마다 규칙을 다시 확인합니다(#76). 나중에 검색을 꺼도 규칙은 남고,
  제거(uninstall) 시 삭제됩니다.
- 네트워크 프로필이 **개인(Private)** 이어야 합니다. 공용(Public) 프로필에서는 규칙과
  무관하게 Windows가 인바운드 멀티캐스트를 막습니다.
- 허브와 PC가 **같은 L2 구간**이어야 합니다(같은 서브넷/VLAN, AP 격리·클라이언트 격리
  없음). 라우터는 239.255.255.250을 구간 사이로 전달하지 않습니다.
- 가상 어댑터(Hyper-V, WSL, VPN 탭)는 시작 시
  `SSDP: <name> did not join 239.255.255.250` 로그를 남길 수 있습니다. 정상입니다 —
  응답기가 해당 어댑터를 건너뛰고 실제 LAN 어댑터만 사용합니다.

자동 검색이 끝내 아무것도 찾지 못해도 **수동 추가**로 동일하게 동작합니다.

---

## 설치

### 1. 채널 가입

드라이버는 SmartThings **채널**로 배포됩니다. 아래 초대 링크를 열고 허브를 등록한 뒤
드라이버를 설치하세요.

> **채널 초대 링크:** `(채널 생성 후 이 자리에 링크를 넣습니다)`
>
> 아직 공개 채널이 없습니다. 레포 소유자가 `smartthings edge:channels:create`로 채널을
> 만들고 초대 링크를 발급하면 이 줄을 교체합니다. 그 전까지는 아래
> [개발](#개발) 절차로 직접 패키징해 설치할 수 있습니다.

### 2. 드라이버 설치

초대 링크 → [Enroll] → 허브 선택 → **SmartThings PC Control** 드라이버 [Install].
SmartThings 앱의 *메뉴 → 설정 → 연결된 서비스 → 허브 → 드라이버*에서 확인할 수 있습니다.

### 3. 장치 추가

**자동 (SSDP, 권장)** — SmartThings 앱에서 **[+] → 기기 추가 → 주변 기기 검색**.
드라이버가 LAN에 `M-SEARCH`를 보내고, 응답한 PC마다 장치를 하나씩 만듭니다. IP·포트·
호스트 이름이 이미 채워진 채로 생성되므로 **시크릿만** 넣으면 끝입니다.

**수동** — 검색이 아무것도 찾지 못하면(서비스에서 검색을 껐거나 멀티캐스트가 막힌 망)
같은 [주변 기기 검색]이 *PC Control (set IP in settings)* 이라는 장치를 하나 만듭니다.
장치 설정을 열어 PC의 IP 주소와 시크릿을 넣으세요. 빈 장치가 쌓이지는 않습니다 — IP가
비어 있는 장치가 남아 있는 동안에는 다시 검색해도 새로 만들지 않습니다.

수동으로 추가한 장치도 첫 상태 조회에 성공하면 PC의 `machine_id`를 기억하므로, 나중에
SSDP가 같은 PC를 찾아도 중복 생성하지 않고 주소만 갱신합니다.

### 4. 환경설정

장치 설정(⋮ → 설정)에서 다음을 조정합니다.

| 설정 | 설명 | 기본값 |
|---|---|---|
| `PC IP address` | PC의 IPv4 주소. **비워 두면** SSDP로 알아낸 주소를 사용합니다. 채워 넣으면 고정 IP로 간주하고 항상 이 값이 우선합니다 | `""` |
| `Follow discovery` | SSDP가 같은 PC를 다른 IP로 알려 오면 주소를 따라갑니다. 끄면 지금 주소에 고정됩니다 | 켬 |
| `Service port` | 서비스의 명령 포트 | `5001` |
| `Secret` | 설정 탭의 시크릿. `X-PC-Secret` 헤더로 보냅니다. **Edge에는 비밀번호 입력 타입이 없어 입력 중 화면에 그대로 보입니다** | `""` |
| `MAC address` | Wake-on-LAN용 MAC. 비워 두면 서비스가 보고한 WoL 가능 어댑터의 MAC을 씁니다 | `""` |
| `WoL broadcast address` | 매직 패킷을 보낼 주소. 공유기가 `255.255.255.255`를 막으면 서브넷 브로드캐스트(예: `192.168.1.255`)를 넣으세요 | `255.255.255.255` |
| `Status poll interval` | 상태 확인 주기 (10초 / 30초 / 1분 / 5분) | `30초` |
| `Switch off action` | 스위치를 끌 때 보낼 명령. 강제 종료를 뺀 나머지는 PC에 설정된 유예를 따릅니다 | `Shut down` |
| `Create display device` | 화면을 켜고 끄는 자식 스위치를 만듭니다. PC가 한 번이라도 응답한 뒤에 나타나고, 끄면 삭제됩니다 | 켬 |
| `Message language` | 상태·예약 문구의 언어. 드라이버는 허브 로케일을 읽을 수 없어 Auto는 영어로 동작합니다 | `Auto (English)` |

> 푸시 구독에 성공해도 폴링 주기는 사용자가 정한 값을 유지합니다. 푸시가 즉시 반영을
> 담당하고 폴링은 안전망입니다.

---

## 사용

### 스위치

- **켜기** → Wake-on-LAN 매직 패킷(즉시 / 2초 / 5초, 포트 7과 9 모두). 상태가
  `waking`으로 바뀌고, PC가 응답하면 `on`이 됩니다. 90초 안에 응답이 없으면 이전
  상태로 돌아가고 "깨우기 실패: WoL 응답 없음"을 표시합니다.
- **끄기** → 환경설정 `Switch off action`의 명령을 보냅니다. 기본은 종료이고, PC에
  유예가 설정돼 있으면 그 유예를 따릅니다(그동안 취소할 수 있습니다).

스위치는 전원 상태가 `on` · `waking` · `shuttingDown`일 때 켜짐으로 보입니다. PC에서
유예를 취소하면 스위치가 다시 켜짐으로 돌아옵니다.

### 전원 상태

`pcPowerState.powerState`는 `on` · `sleeping` · `hibernated` · `off` · `waking` ·
`shuttingDown` · `unknown` 중 하나입니다. 서비스가 종료·절전 직전에 푸시를 보내므로
폴링 주기를 기다리지 않고 바뀝니다.

### 명령 실행

`pcCommand.execute(command, mode, minutes)` — 명령 9종
(`shutdown` `forceshutdown` `restart` `hibernate` `suspend` `lock` `turnscreenoff`
`turnscreenon` `ping`), 모드 3종(`default` 설정된 유예를 따름 / `immediate` 유예 없이 /
`grace` 유예를 강제), 분 0~1440.

`minutes`가 0보다 크면 실행 대신 **예약**이 걸립니다. 마지막으로 실행한 명령은
`lastCommand`에 "shutdown · SmartThings · 23:05"처럼 남습니다.

### 예약과 취소

`pcSchedule`이 대기 중인 예약을 보여 줍니다: `active`, `command`,
`remainingSeconds`, `executeAt`(로컬 `HH:MM`), `origin`(앱 · 원격 · 텔레그램 ·
SmartThings). 예약은 PC당 하나이고, 새 예약은 기존 것을 대체합니다.

- `pcSchedule.schedule(command, minutes)` — 예약합니다. 프리셋 5/15/30/60/120분이
  선택지로 나오지만 1~1440분 아무 값이나 쓸 수 있습니다.
- `pcSchedule.cancel()` — 취소합니다. PC 쪽에는 "SmartThings에서 취소됨"으로 기록됩니다.

앱·트레이 토스트·텔레그램에서 취소해도 푸시로 즉시 반영됩니다. 반대도 마찬가지입니다.

### 상태 카드

`pcStatus`는 조용한 실패를 드러내기 위한 카드입니다.

| 속성 | 내용 |
|---|---|
| `connection` | `ok` · `unauthorized`(시크릿 불일치 또는 허브가 허용 목록 밖) · `unreachable` · `incompatible` |
| `serviceVersion` | PC에서 돌고 있는 서비스 버전 |
| `updateAvailable` | 서비스에 새 릴리스가 있는지 |
| `wolReady` | WoL 가능 어댑터가 하나라도 있는지 |
| `lastSeen` | 마지막으로 성공한 상태 조회 시각 |
| `message` | 사람이 읽는 안내 **한 줄**. 여러 개가 겹치면 오류 > 호환성 > WoL 미준비 > 업데이트 > 시크릿 없음 순으로 하나만 고릅니다 |

### 디스플레이 자식 장치

`Create display device`가 켜져 있으면 "`호스트이름` Display"라는 스위치가 함께
생깁니다. 켜면 `turnscreenon`, 끄면 `turnscreenoff`를 부모 PC에 보내고, 상태는 서비스가
보고한 `display` 값을 따릅니다(`unknown`이면 건드리지 않습니다).

### 세션 정보 (선택)

PC의 GUI 네트워크 탭에서 *세션 정보 노출*을 켜면 `pcSession`이 잠금 여부와 유휴
시간을 보여 줍니다. 기본은 꺼져 있고, 꺼져 있는 동안 드라이버는 이 capability의
이벤트를 아예 내보내지 않습니다.

### 자동화 예시

**1. 자정 취침 예약** — 잊고 켜 둔 PC를 30분 뒤 종료하되 취소할 여지를 둡니다.

```
조건(If)  : 시각이 00:00이고
            PC의 Power state 가 on
동작(Then): PC 의 pcSchedule.schedule(command: shutdown, minutes: 30)
```

30분 카운트다운이 SmartThings·트레이 토스트·텔레그램에 동시에 뜨고, 어디서든 취소하면
모든 곳에서 사라집니다.

**2. 외출하면 잠그고 화면 끄기** — 종료까지는 하지 않습니다.

```
조건(If)  : 구성원 전원이 집을 떠남
동작(Then): PC 의 pcCommand.execute(command: lock, mode: immediate)
            PC Display 스위치 끄기
```

**3. 책상 주변기기 전원 연동** — 모니터·스피커 스마트플러그를 PC에 맞춥니다.

```
조건(If)  : PC의 Power state 가 waking 또는 on 으로 바뀜
동작(Then): 책상 플러그 켜기

조건(If)  : PC의 Power state 가 off 또는 sleeping 으로 바뀜
동작(Then): 책상 플러그 끄기
```

`waking`을 조건에 넣으면 WoL로 깨우는 동안 모니터가 미리 켜져 부팅 화면을 놓치지 않습니다.

---

## 여러 PC

같은 LAN에 서비스를 설치한 PC가 여러 대 있어도, 허브 하나가 여러 PC를 다루거나 여러
허브가 한 PC를 다루어도 동작합니다(설계 문서 §13).

- **식별자는 `machine_id`**(Windows MachineGuid)입니다. SSDP·상태·푸시 본문 최상위에
  모두 실려 있어 드라이버가 이 값으로 장치를 찾습니다. 같은 `machine_id`를 이미 가진
  장치가 있으면 새로 만들지 않고 주소만 갱신합니다.
- **시크릿·MAC·브로드캐스트 주소는 장치별 환경설정**입니다. 서브넷이 다른 PC에는 그
  서브넷의 브로드캐스트 주소를 넣으세요.
- **DHCP로 IP가 바뀌어도** `Follow discovery`가 켜져 있으면 따라갑니다. `PC IP address`를
  채워 넣으면 고정으로 간주하고 SSDP가 알려 주는 주소를 무시합니다. 연결이 끊긴 장치는
  다음 폴링 전에 한 번(최대 5분에 한 번) 표적 검색으로 주소를 다시 찾습니다.
- **폴링은 분산됩니다.** 장치가 N개면 장치 ID 해시로 시각을 흩어 같은 초에 몰리지
  않게 합니다. 푸시 리스너는 드라이버당 하나이고, 구독은 PC별로 하나입니다.
- **MachineGuid 복제 주의.** 한 PC를 이미지로 복제하면 두 대의 `machine_id`가 같아져
  SSDP에서 한 장치로 합쳐집니다. 드라이버는 "같은 machine_id, 다른 hostname"을 만나면
  `pcStatus.message`로 경고합니다. 해결은 한쪽에서 MachineGuid를 재생성하는 것입니다
  (Sysprep, 또는 `HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid` 직접 교체).
- **텔레그램은 이 드라이버와 무관**합니다. PC 서비스의 텔레그램은 **봇 하나 = PC 하나**
  전제로 유지됩니다. 여러 PC가 같은 봇 토큰을 쓰면 `getUpdates`가 409 Conflict를 내고
  서비스가 로그·GUI로 경고합니다. **PC마다 봇을 따로 만드세요.** 여러 PC를 봇 하나로
  묶는 것은 별도 프로젝트인 허브 에이전트의 몫입니다
  ([`docs/design/hub-agent.md`](../docs/design/hub-agent.md)).

---

## 개발

### 저장소 구성

```
config.yml               드라이버 메타 (name, packageKey, permissions: lan)
profiles/pc.yml          메인 프로필: capability + 환경설정 (§5.1, §5.4)
profiles/pc-display.yml  디스플레이 자식 프로필: 스위치 하나 (§5.2)
capabilities/            커스텀 capability 정의 + 프레젠테이션 (§5.1, §5.3)
src/
  init.lua               진입점: lifecycle 과 capability 핸들러만
  caps.lua               커스텀 capability id, NAMESPACE 상수 한 곳
  client.lua             /st/v1 HTTP 클라이언트, 오류 분류 (§4, §6.1)
  discovery.lua          SSDP 검색, 수동 추가, 다중 PC 식별 (§4.6, §13)
  display.lua            디스플레이 자식 장치 (§5.2)
  poll.lua               폴링 타이머, health, 이벤트 발행, 분산 (§6.1, §13.3)
  push.lua               푸시 리스너와 구독 (§4.5, §6.4)
  state.lua              순수 함수: status JSON → 이벤트, 전원 상태 머신 (§6.2)
  wol.lua                매직 패킷, 깨우기 시퀀스 (§6.3)
  i18n.lua               사용자에게 보이는 속성 문자열의 ko/en (§6.5)
  version.lua            드라이버 버전 (User-Agent, 릴리스 태그 검증)
tests/
  run.lua                테스트 러너
  syntax.lua             모든 모듈을 실행 없이 컴파일
  helpers.lua            단언문과 가짜 장치
  mocks/                 log, ltn12, cosock, st.json, st.utils, st.driver, st.capabilities
  *_test.lua
tools/
  lua.js                 fengari 기반 `lua <file>` 러너
  apply-namespace.js     네임스페이스 일괄 적용
  create-capabilities.sh 커스텀 capability 5종 생성 (CLI)
```

허브에서는 `src/`가 패키지 루트라, 모듈끼리는 항상 이름만으로 require 합니다
(`require "state"`, `require "src.state"`가 아님).

### 테스트

설치할 Lua 인터프리터가 없습니다. [fengari](https://fengari.io/)는 JavaScript로 구현된
Lua 5.3이고, 허브와 같은 방언을 실행합니다.

```sh
cd edge
npm install                        # 또는: bun install
npm test                           # 또는: bun tools/lua.js tests/run.lua
npm run syntax                     # 또는: bun tools/lua.js tests/syntax.lua
```

`package.json`만 커밋하고 락파일은 두지 않습니다. 의존성이 fengari 하나뿐이고
정확한 버전으로 고정돼 있어서, CI도 `npm ci`가 아니라 `npm install`을 씁니다.

`run.lua`는 `tests/*_test.lua`를 찾아 각 파일이 돌려주는 테이블의 `test_*` 함수를 모두
호출합니다. 테스트 파일을 추가할 때 등록할 곳은 없습니다. 허브 모듈(`st.*`, `cosock`,
`ltn12`)은 로컬에 없으므로 `tests/mocks/`에서 preload 되고, 소켓을 건드릴 만한 것은
모두 주입받습니다(`client.get_status(device, { http = fake })`,
`wol.send(mac, broadcast, { socket = fake })`). `cosock.asyncify`는 테스트에서 일부러
예외를 던져 실제 HTTP 호출이 새어 나가지 못하게 합니다.

로직의 핵심인 `state.lua`는 순수 함수입니다 — 이벤트를 내보내는 대신
`{ cap, attr, value }` 레코드를 돌려주므로 상태 매핑과 전원 상태 머신 전체를 허브 없이
검증할 수 있습니다.

`tools/lua.js`는 `fs`·`path`만 쓰는 평범한 CommonJS입니다(bun 전용 API 없음). 그래서
CI의 node와 로컬의 bun에서 같은 명령이 그대로 돕니다.

### 커스텀 capability와 네임스페이스

`capabilities/`에 설계 문서 §5.1의 커스텀 capability 5종이 각각 정의 + 프레젠테이션
두 파일로 들어 있습니다.

```
pcPowerState.json               정의        -> smartthings capabilities:create -i <file>
pcPowerState.presentation.json  프레젠테이션 -> smartthings capabilities:presentation:create
```

`pcPowerState`는 전원 상태, `pcCommand`는 명령 실행, `pcSchedule`은 예약 표시·조작,
`pcStatus`는 연결·버전·메시지 카드, `pcSession`은 선택 항목인 잠금·유휴 블록입니다.

`src/caps.lua`, `profiles/pc.yml`, `capabilities/*.json`은 모두 **플레이스홀더
네임스페이스 `pccontrol00000`** 을 씁니다. 진짜 네임스페이스는 계정 소유자가 커스텀
capability를 만들 때 SmartThings가 발급합니다.

```sh
cd edge
./tools/create-capabilities.sh            # 5종 생성 + 프레젠테이션, 네임스페이스 출력
node tools/apply-namespace.js <namespace> # 모든 파일에 일괄 반영 (bun 도 가능)
npm test
```

`apply-namespace.js`는 현재 네임스페이스를 `src/caps.lua`에서 읽어 바꾸므로 **여러 번
돌려도 안전하고**, 나중에 다른 계정으로 옮길 때도 그대로 쓸 수 있습니다.
`--dry-run`으로 바뀔 파일만 먼저 볼 수 있습니다. 8~20자 소문자·숫자가 아닌 값은
거부합니다.

`create-capabilities.sh`는 **계정당 한 번만** 실행하세요. `capabilities:create`에는
"있으면 갱신" 모드가 없어, 다시 돌리면 같은 이름의 capability가 하나 더 생깁니다.
정의를 고칠 때는 `smartthings capabilities:update <id> <version> -i <file>`을 씁니다.

`tests/capabilities_test.lua`가 JSON과 Lua의 아귀를 맞춰 검사합니다(같은 id, `state`가
쓰는 것과 같은 속성, `init.lua` 핸들러와 같은 명령, 정의되지 않은 것을 참조하지 않는
프레젠테이션). 다만 SmartThings가 이 파일들의 **형식**을 받아들일지는 CLI만이 압니다.
생성할 때 확인할 부분:

- 정의의 `id`, `version`, `status`, `ephemeral` — CLI가 무시하거나 거부하고 자기 값을
  넣을 수 있습니다.
- 모든 속성에 `enumCommands: []`를 명시했습니다(enum이 아닌 속성 포함).
- `pcCommand.execute`와 `pcSchedule.schedule`의 상세 화면·자동화 동작 항목이
  `displayType: "multiArgCommand"`와 인자별 `displayType`을 쓰고, `minutes` 프리셋
  (5/15/30/60/120)이 명령 인자에 `alternatives`를 허용한다는 가정에 기대고 있습니다.
  둘 중 하나라도 거부되면 `minutes`를 `numberField`로 낮추세요.
- `state`, `list`, `pushButton`, `numberField`가 프레젠테이션의 유효한 `displayType`
  이라고 가정하고 있습니다.

### CLI 패키징

```sh
smartthings edge:drivers:package edge/          # 업로드하고 driverId/version 출력
smartthings edge:drivers:package edge/ --build-only driver.zip   # zip 만 만들기
smartthings edge:channels:assign <driverId> <version> --channel <channelId>
smartthings edge:drivers:install <driverId> --hub <hubId>        # 채널 등록 후
```

로그는 `smartthings edge:drivers:logcat <driverId> --hub-address <허브IP>`로 봅니다.

**CI** — [`.github/workflows/edge.yml`](../.github/workflows/edge.yml)이 `edge/**`가
바뀐 push/PR마다 테스트와 문법 검사를 돌립니다. `edge-vX.Y.Z` 태그를 밀면 태그와
`src/version.lua`의 문자열이 같은지 확인한 뒤 패키징 → 채널 배정 → zip을 릴리스에
첨부합니다. `-rc`가 붙은 태그는 프리릴리스로 올라갑니다. 저장소 시크릿
`SMARTTHINGS_TOKEN`(PAT)과 `ST_CHANNEL_ID`가 필요합니다.

### 온허브 검증 체크리스트

로컬 테스트로는 확인할 수 없는, 허브 런타임에서만 드러나는 것들입니다. 실기에서 한 번씩
짚어 보세요.

**런타임 가정**

- [ ] `cosock.socket.tcp()`를 `0.0.0.0:0`에 바인딩하고 `getsockname()`으로 얻은 포트에
      LAN이 도달하는지. `cosock.spawn` 안의 `accept()`가 다른 태스크를 굶기지 않는지.
- [ ] 허브 IP: `driver:get_ip()`가 있는 펌웨어인지, 없으면 PC를 향해 `setpeername`한
      UDP 소켓의 `getsockname()` 폴백이 맞는 주소를 주는지. (서비스가 콜백 호스트와
      요청 출처 IP가 같기를 요구하므로, 틀리면 subscribe가 `400`으로만 나타납니다.)
- [ ] 멀티캐스트: 허브가 Edge 드라이버의 239.255.255.250:1900 송신과, 같은 소켓으로
      오는 유니캐스트 응답 수신을 허용하는지.
- [ ] `EDGE_CHILD` 자식 생성: `profile`, `parent_device_id`,
      `parent_assigned_child_key`. `try_delete_device`가 device에 있는지 driver에
      있는지 양쪽 다인지.
- [ ] `pc-display.yml`의 `categories` 값으로 `Switch`가 유효한지.
- [ ] `device:set_field(..., { persist = true })`가 드라이버 재시작 뒤에도 검색한
      주소와 `machine_id`를 유지하는지.

**기능 확인**

- [ ] SSDP로 PC가 발견되고 IP·포트·호스트 이름이 채워진 채 장치가 생기는지.
- [ ] 시크릿만 넣으면 `connection=ok`, 틀리면 `unauthorized`가 되는지.
- [ ] 스위치 끄기 → 유예 카운트다운이 SmartThings·토스트·텔레그램에 동시에 뜨는지.
- [ ] 토스트에서 취소 → SmartThings 예약 카드가 즉시 사라지고 스위치가 켜짐으로 복귀.
- [ ] 절전 → `sleeping`, 종료 → `shuttingDown` → `off`, WoL → `waking` → `on`.
- [ ] 디스플레이 자식 스위치가 실제로 화면을 끄고 켜는지(`turnscreenon` 포함).
- [ ] PC를 껐다 켜도(서비스 재시작) 구독이 되살아나는지.
- [ ] PC 두 대를 추가했을 때 서로 섞이지 않는지.
- [ ] 커스텀 capability가 없는 상태(플레이스홀더 네임스페이스)에서도 스위치·refresh·
      health가 살아 있는지.

---

## 문제 해결

| 증상 | 확인할 것 |
|---|---|
| **검색해도 아무것도 안 나옴** | PC의 GUI 네트워크 탭에서 *자동 검색(SSDP) 허용*이 켜져 있는지. UDP 1900 방화벽 규칙과 네트워크 프로필(개인). 허브와 PC가 같은 서브넷인지. Wi-Fi AP 격리. 안 되면 수동 추가로 넘어가세요 |
| **`connection = unauthorized`** | 장치 설정의 시크릿과 PC 설정 탭의 시크릿이 같은지. 메시지가 "허브가 허용 목록에 없습니다"이면 시크릿이 아니라 GUI의 *허용 목록*이 문제입니다 — [현재 허브 추가]를 누르거나 목록을 비우세요 |
| **`connection = unreachable`** | PC가 켜져 있고 서비스가 돌고 있는지(`pc-control.exe status`). IP·포트가 맞는지. TCP 5001 방화벽. IP가 바뀌었다면 `PC IP address`를 비우고 `Follow discovery`를 켜세요 |
| **`connection = incompatible`** | 서비스가 v1.1.0 미만입니다. 서비스를 업데이트하세요. 반대로 드라이버가 낡았다는 메시지면 채널에서 드라이버를 업데이트합니다 |
| **스위치 켜기가 안 먹음(WoL)** | `wolReady`가 false면 PC 어댑터의 Wake-on-LAN이 꺼져 있습니다(장치 관리자 → 전원 관리). 공유기가 `255.255.255.255`를 막으면 `WoL broadcast address`를 서브넷 브로드캐스트로. 절전이 아니라 완전 종료 상태라면 메인보드의 "PCIE로 깨우기"와 빠른 시작(Fast Startup) 해제도 필요합니다 |
| **상태가 늦게 갱신됨** | 푸시 구독이 실패하고 폴링만 도는 상태일 수 있습니다. `lastSeen`을 보고, 폴링 주기를 줄여 보세요. 허브 IP 판단이 틀리면 서비스 로그에 subscribe `400`이 남습니다 |
| **장치가 두 개로 보임** | 수동 추가 뒤 SSDP가 같은 PC를 다시 찾은 경우입니다. 첫 상태 조회에 성공해야 `machine_id`를 학습하므로, 시크릿을 넣어 `ok`로 만든 뒤 남는 쪽을 지우세요 |
| **PC 두 대가 한 장치로 합쳐짐** | 이미지 복제로 MachineGuid가 같습니다. 한쪽에서 재생성하세요(위 [여러 PC](#여러-pc)) |
| **커스텀 타일이 안 보임** | 네임스페이스가 아직 플레이스홀더입니다. `create-capabilities.sh` → `apply-namespace.js` → 재패키징 순서로 처리하세요. 그동안에도 스위치·새로고침은 동작합니다 |
| **디스플레이 자식이 안 생김** | `Create display device`가 켜져 있어야 하고, PC가 최소 한 번 응답해야 생깁니다 |
| **텔레그램이 다른 PC 것과 섞임** | 봇 토큰 하나를 여러 PC가 공유하고 있습니다(409 Conflict). PC마다 봇을 분리하세요 |

드라이버 로그: `smartthings edge:drivers:logcat <driverId> --hub-address <허브IP>`.
서비스 로그: exe 옆 `service.log`, 또는 데스크톱 앱의 로그 탭.

---

## English

An Edge driver (Lua 5.3) for the PC Control service. It speaks the service's
`/st/v1` protocol, specified in
[`docs/design/edge-driver.md`](../docs/design/edge-driver.md) (Korean) — the
binding contract for both sides.

Where the [PCControl driver](https://github.com/toddaustin07/PCControl) offers a
switch and a ping, this one surfaces what the service already knows: a real
power state (`on` / `sleeping` / `hibernated` / `off` / `waking` /
`shuttingDown`), the grace countdown with its origin, schedule and cancel,
connection/version/WoL diagnostics, a display child device and SSDP discovery.
The legacy `/{secret}/{command}` path is untouched, so **existing PCControl
users need to change nothing**.

**Requirements** — a SmartThings hub, PC Control service **v1.1.0 or newer**
(that is where `/st/v1` appears), hub and PC on the same subnet, and a secret
(optional but strongly recommended). SSDP additionally needs inbound **UDP 1900**
allowed on the PC — `pc-control.exe install` adds the rule and the service
re-checks it at every start while discovery is on — a **Private** network
profile, and no AP/client isolation between hub and PC.

**Install** — join the distribution channel (invite link to be added once the
channel exists), install the driver onto the hub, then **Add device → Scan
nearby**. SSDP fills in IP, port and hostname, so only the secret is left to
type. Without SSDP the same scan creates one device labelled *PC Control (set IP
in settings)* for you to fill in by hand; it adopts the PC's `machine_id` on its
first successful poll, so a later SSDP hit updates it rather than duplicating it.

**Preferences** — `ipAddress` (empty = follow SSDP), `followDiscovery`, `port`
(5001), `secret` (sent as `X-PC-Secret`; Edge has no password field, so it is
visible while typing), `macAddress`, `wolBroadcast`, `pollInterval`
(10 s/30 s/1 min/5 min), `offAction`, `createDisplayDevice`, `language`.

**Use** — switch on sends Wake-on-LAN (immediately, +2 s, +5 s, ports 7 and 9);
switch off sends `offAction` and honours the PC's grace period.
`pcCommand.execute(command, mode, minutes)` runs any of the nine commands, or
schedules it when `minutes > 0`. `pcSchedule` shows the pending schedule with a
countdown and origin and offers `cancel()` / `schedule()`. `pcStatus` carries
`connection`, `serviceVersion`, `updateAvailable`, `wolReady`, `lastSeen` and a
one-line `message`. A display child switch runs `turnscreenon` / `turnscreenoff`.

**Several PCs** — the identity is `machine_id` (Windows MachineGuid), carried at
the top level of SSDP, status and push bodies, so one hub routes several PCs
correctly and a known machine_id updates an address instead of creating a second
device. Secret, MAC and broadcast address are per-device preferences. Polls are
staggered. Cloning a Windows image duplicates the MachineGuid and merges two PCs
into one device — regenerate it on one of them. Telegram stays **one bot per
PC**; sharing a token yields `getUpdates` 409 conflicts.

**Development**

```sh
cd edge
npm install                          # or: bun install
npm test                             # or: bun tools/lua.js tests/run.lua
npm run syntax
./tools/create-capabilities.sh       # once per account: creates the 5 capabilities
node tools/apply-namespace.js <ns>   # writes the assigned namespace everywhere
```

`tools/lua.js` runs the suite under [fengari](https://fengari.io/) (Lua 5.3 in
JavaScript) using only `fs`/`path`, so node and bun both work. No lockfile is
committed — fengari is pinned exactly — so CI uses `npm install`, not `npm ci`.
`.github/workflows/edge.yml` tests every `edge/**` push and PR and, on an
`edge-vX.Y.Z` tag, checks the tag against `src/version.lua`, packages the driver,
assigns it to the channel and attaches the zip to the release. It needs the
`SMARTTHINGS_TOKEN` and `ST_CHANNEL_ID` repository secrets.

The ids in `src/caps.lua`, `profiles/pc.yml` and `capabilities/*.json` use the
placeholder namespace `pccontrol00000` until `apply-namespace.js` replaces it;
until then `caps.load` skips capabilities it cannot resolve and `switch`,
`refresh` and `healthCheck` keep working.
