# SmartThings Edge 드라이버

PC Control 서비스를 위한 전용 SmartThings Edge 드라이버다.
허브 안에서 로컬로 돌면서 PC의 `/st/v1` API로 이야기한다.

- **실제 전원 상태** — 켜짐 · 절전 · 최대 절전 · 꺼짐 · 깨우는 중 · 종료 대기. 스위치가 실제 상태와 어긋나지 않는다.
- **유예와 예약이 보인다** — 남은 시간, 실행 시각, 출처(SmartThings · 앱 · 텔레그램), 취소.
- **IP를 손으로 넣지 않는다** — SSDP 검색으로 주소 · 포트 · 호스트 이름이 채워진 채 장치가 생기고, DHCP로 주소가 바뀌어도 따라간다.
- **Wake-on-LAN** — 스위치를 켜면 PC가 고른 랜카드로 매직 패킷을 보내고 결과를 말해 준다.
- **미디어 · 프리셋 · 메시지 · 잠들지 않기 · 배터리 · 감시 목록**(드라이버 1.1, 서비스 v1.2.0).
- **조용한 실패가 없다** — 시크릿 불일치, 연결 불가, 버전 비호환, WoL 미준비를 한 줄로 알려 준다.
- **여러 PC** — Windows MachineGuid로 구분하므로 허브 하나가 여러 PC를 다뤄도 섞이지 않는다.

## 요구 사항

| | |
|---|---|
| 허브 | Edge 드라이버를 실행할 수 있는 SmartThings 허브 |
| 서비스 | PC Control **v1.1.0 이상**(낮으면 `버전 불일치`). 드라이버 1.1의 새 기능은 **v1.2.0**(그 전에는 상태 줄에 `앱 업데이트 필요`, 새 기능 줄에 `PC 앱 v1.2.0 필요`) |
| 네트워크 | 허브와 PC가 같은 서브넷/VLAN, PC의 네트워크 프로필은 **개인(Private)** |
| 방화벽 | 인바운드 TCP 5001, UDP 1900. 둘 다 `install`이 만든다 |
| 시크릿 | 권장. 비어 있으면 LAN의 누구나 PC를 제어할 수 있고 드라이버가 경고한다 |

라우터는 SSDP 멀티캐스트를 서브넷 사이로 전달하지 않는다.
**검색이 장치를 추가하는 유일한 경로**이므로 추가는 같은 서브넷에서 한다.
그 뒤에는 장치 설정의 `PC IP 주소`로 주소를 고정할 수 있다.

## 설치

1. 채널 **Protomothis**의 초대 링크를 열고 [Enroll] → 허브를 고른다: <https://bestow-regional.api.smartthings.com/invite/Kr2zNWYgpp2A>
2. 채널의 **SmartThings PC Control**을 [Install] 한다(앱의 *메뉴 → 설정 → 연결된 서비스 → 허브 → 드라이버*에서 확인).
3. PC의 데스크톱 앱 **설정 탭**에서 시크릿을 정하고, **SmartThings 탭**의 검색 상태가 `검색 응답기 켜짐 · 방화벽 규칙 OK`인지 본다.
4. **PC와 PC Control이 켜진 상태에서** SmartThings 앱 **[+] → 기기 추가 → 주변 기기 검색**. 응답한 PC마다 장치가 생긴다.
5. 장치 설정에 **시크릿만** 넣는다.

장치의 **모델**은 `PC Control · 58bff996`처럼 PC 식별자의 앞 8자를 담는다.
데스크톱 앱 SmartThings 탭의 **이 PC의 ID**와 같은 값이다.

### 검색이 안 될 때

응답이 0대면 장치가 생기지 않는다. 순서대로 확인한다.

1. PC와 PC Control이 켜져 있는가(트레이 아이콘, 또는 `smartthings-pc-control.exe status`).
2. 방화벽 규칙 *SmartThings PC Control SSDP*와 네트워크 프로필 **개인**. 검색 상태가 `방화벽 규칙 없음`이면 서비스를 다시 시작한다.
3. SmartThings 탭의 **마지막 검색 요청** 시각이 검색을 누를 때 갱신되는가. 아니면 PC가 아니라 네트워크 문제다(다른 서브넷, Wi-Fi 클라이언트 격리).
4. **허브 허용 목록**이 비어 있지 않은데 이 허브가 없으면 [현재 허브 추가]를 누르거나 목록을 비운다.

## 화면

**대시보드** — 타일에 전원 상태, 토글은 스위치다.

**상세 화면** — 위에서부터 다음 줄이 온다.

| 줄 | 보여 주는 것 / 하는 일 |
|---|---|
| 상태 카드 | 전원 상태 · 마지막 실행 · 예약 요약 · 세션(옵트인) · 상태(`연결됨 · 3일 2시간`, `응답 없음 · 마지막 확인 12분 전`) · 버전(`v1.2.0 · 드라이버 1.1`) |
| 조작 카드 | 명령(깨우기 · 절전 · 최대 절전 · 재시작 · 종료 · 잠금 · 화면 끄기/켜기) · 예약할 명령 · 예약 시간(5분~3일, 취소) |
| 미디어 | 곡 정보 · 재생/일시정지/정지 · 이전/다음 곡 · 볼륨 · 음소거(SmartThings 표준 capability) |
| 프리셋 | 슬롯 목록(`프리셋 1`…`프리셋 10`)과 `프리셋 목록` 줄(`1 게임 모드 · 2 방송 시작`) |
| 감시 목록 | 별도 카드. 실행 중인 앱 가운데 번호가 가장 작은 것(`Steam`, `Steam 외 1`, `없음`, `꺼짐`), `감시 1`…`감시 5`(`실행 중` / `꺼짐` / `비어 있음`), 이름 줄(`1 Steam · 3 OBS`). 메인 화면의 카드에는 앞의 셋이 보인다 |
| PC에 메시지 보내기 | 문구를 넣으면 PC 화면에 알림이 뜬다. 줄에는 마지막으로 보낸 문구가 남는다 |
| 잠들지 않기 | 별도 카드의 스위치 |
| 배터리 | **노트북에만.** 잔량(%)과 전원 공급원 |

장치의 화면은 **추가한 시점의 프로필로 굳는다.**
드라이버는 첫 `init`에서 장치를 현재 프로필(`pc*.v10`)로 옮기고, 비는 줄은 30초 안에 다시 채운다.

## 사용

**스위치** — 켜면 매직 패킷을 세 번(즉시 · 2초 · 5초) 보내고 상태가 `깨우는 중`이 된다. 90초 안에 응답이 없으면 "깨우기 실패"를 표시한다.
끄면 환경설정 `스위치 끄기 동작`(기본 종료)을 보내고, PC의 유예를 따른다.
PC에서 유예를 취소하면 스위치가 다시 켜짐으로 돌아온다.

**어느 랜카드로 깨우는가** — PC가 정한다(데스크톱 앱 **SmartThings 탭 → WoL 어댑터**).
`MAC 주소` 환경설정을 채우면 그 값이 이긴다.
고른 어댑터의 WoL이 꺼져 있으면 상태 줄이 `연결됨 · WoL 꺼짐 (이더넷)`처럼 어댑터 이름을 댄다.

**명령** — 목록에서 고르면 바로 나간다. 유예를 따를지는 `버튼 실행 방식`이 정한다.
목록은 늘 `명령 선택…`에 머문다. 화면 켜기·끄기는 로그인된 세션이 있어야 한다.

**PC가 꺼지거나 켜지는 중** — 명령 목록이 `종료 진행 중…`·`켜는 중…`이 되고 그동안의 명령은 보내지 않는다.
새로고침, 예약 취소, 스위치 켜기(= 유예 취소)는 통과한다.
3일 뒤 종료 같은 긴 예약은 전환이 아니라서 아무것도 막지 않는다.

**예약** — **예약할 명령**과 **예약 시간**을 고른다. 예약은 PC당 하나이고 새 예약이 기존 것을 바꾼다.
자동화에서 `minutes`는 `"30"`처럼 문자열 목록 값이다.

**볼륨 · 미디어** — PC에 로그인한 사용자의 세션에서 동작한다.
보낼 수 없으면 상태 줄이 이유를 말한다: `PC 앱 v1.2.0 필요` · `사용자 없음` · `미디어 제어 꺼짐` · `이 PC에서 지원 안 함` · `PC에서 실행 실패`.
곡 정보는 PC 앱 **공유 탭**의 **재생 정보 공유**를 켰을 때만 온다.

**프리셋** — 무엇을 실행할지는 PC 앱에만 있고, SmartThings는 슬롯 번호만 보낸다.
실행하면 목록이 5초 동안 `프리셋 3 실행함`을 보인다. 비어 있는 슬롯은 보내지 않고 `프리셋 7 비어 있음`을 띄운다.

**PC에 메시지 보내기** — 상세 화면이나 루틴 동작에 문구(200자까지)를 넣는다. 제목은 `SmartThings`다.
보내지 못하면 메시지 줄이 `PC 알림 꺼짐` · `사용자 없음` · `잠시 후 다시`(분당 10회) 가운데 하나를 띄운다.

**잠들지 않기** — 켜면 `잠들지 않기 시간`(분, 0 = 끌 때까지) 동안 PC가 자동 절전하지 않는다.
절전·종료 명령은 막지 않는다. 표준 스위치라 루틴의 동작·조건으로 쓴다.

**노트북 배터리** — PC가 배터리가 있다고 연속 두 번 알리면 같은 아이콘의 배터리 프로필로 옮긴다. 데스크톱에는 빈 카드가 없다.

**감시 목록** — PC 앱 공유 탭의 감시 목록(슬롯 1–5)이 PC 장치의 `감시 목록` 카드에 그대로 보인다. 루틴 조건은 PC 장치의 `감시 1`…`감시 5`이고 값은 `실행 중` / `꺼짐`이다. 어느 앱이 몇 번인지는 카드의 이름 줄(`1 Steam · 3 OBS`)과 PC 앱이 말한다.

- 번호가 작을수록 우선한다. 앱을 다른 번호로 옮기면 그 번호를 쓰는 루틴이 가리키는 앱도 바뀐다.
- 빈 번호는 `비어 있음`이다. 루틴 조건에는 나오지 않는다.
- 감지를 잠시 끄거나 PC가 꺼져 있거나 연결이 끊기면 슬롯은 마지막 값을 그대로 둔다. 가짜 "꺼지면" 루틴이 돌지 않는다.
- 시험판(Dev 채널)이 앱마다 만들던 장치는 드라이버가 지운다. 지워지지 않으면 SmartThings 앱에서 직접 삭제한다.

### 자동화 예시

```
조건(If)  : 시각이 00:00 이고 PC 의 전원 상태가 켜짐
동작(Then): PC 의 pcDefer.schedule (minutes: 30, command: shutdown)

조건(If)  : PC 의 전원 상태가 깨우는 중 또는 켜짐 으로 바뀜
동작(Then): 책상 플러그 켜기

조건(If)  : PC 의 감시 1 이 실행 중   (감시 목록 1번 = Steam)
동작(Then): 거실 조명 장면 "게임"

조건(If)  : 현관문이 열림
동작(Then): PC 의 PC에 메시지 보내기 ("현관문이 열렸습니다")
```

## 환경설정

장치 화면 오른쪽 위 **⋮ → 설정**.

| 설정 | 설명 | 기본값 |
|---|---|---|
| PC IP 주소 | 비워 두면 검색으로 알아낸 주소를 쓴다. 채우면 이 값이 이긴다 | `""` |
| 검색 따라가기 | 같은 PC가 다른 IP로 응답하면 따라간다 | 켬 |
| 서비스 포트 | 서비스의 명령 포트 | `5001` |
| 시크릿 | PC 설정 탭의 시크릿. Edge에 비밀번호 입력 타입이 없어 입력 중 화면에 보인다 | `""` |
| MAC 주소 | 비워 두면 PC가 고른 WoL 어댑터의 MAC | `""` |
| WoL 브로드캐스트 | 공유기가 `255.255.255.255`를 막으면 서브넷 브로드캐스트(예: `192.168.1.255`) | `255.255.255.255` |
| 상태 확인 주기 | 10초 / 30초 / 1분 / 5분. 푸시가 즉시 반영을 맡고 폴링은 안전망이다 | 30초 |
| 스위치 끄기 동작 | 스위치를 끌 때 보낼 명령 | 종료 |
| 버튼 실행 방식 | 명령 목록이 PC의 유예를 따를지, 즉시 실행할지 | 유예 따름 |
| 문구 언어 | 상태 문구의 언어. 허브 로케일을 읽을 수 없어 자동은 한국어 | 자동 |
| 아이콘 | 기타 · 모니터 · 스위치 · 플러그 · TV · 프로젝터 · 네트워크 · 허브 · 홈시어터 · 리모컨. 다시 추가할 필요가 없다 | 기타 |
| 잠들지 않기 시간 | 분, 최대 1440. 0이면 끌 때까지 | 60 |

## 문제 해결

| 증상 | 확인할 것 |
|---|---|
| 명령 뒤 회전 표시 후 오류 | 드라이버가 오래됐다. 채널에서 업데이트하고 버전 줄을 확인한다 |
| 줄에 "-"만 보임 | 프로필 이전 직후일 수 있다. 몇십 초 기다리거나 [새로 고침]. 계속되면 장치를 지우고 다시 추가한다 |
| `시크릿 불일치` | 장치 설정과 PC 설정 탭의 시크릿이 같은지. "허브가 허용 목록에 없습니다"면 SmartThings 탭의 허용 목록 문제다 |
| `응답 없음` | PC와 서비스가 켜져 있는지, IP · 포트 · TCP 5001 방화벽. IP가 바뀌었으면 `PC IP 주소`를 비우고 `검색 따라가기`를 켠다 |
| `버전 불일치` | 서비스가 v1.1.0 미만이다 |
| 스위치 켜기가 안 먹음 | 상태 줄에 적힌 어댑터의 WoL 설정, SmartThings 탭의 WoL 어댑터 선택, `WoL 브로드캐스트`. 완전 종료에서 깨우려면 메인보드 설정과 빠른 시작 해제도 필요하다 |
| 장치가 두 개 | 이미지로 복제한 PC는 MachineGuid가 같다. 한쪽에서 Sysprep을 돌리거나 `MachineGuid`를 새로 만든다 |
| 커스텀 줄이 안 보임 | 커스텀 capability가 계정에 없다. 스위치와 새로 고침은 그래도 동작한다 |

로그: 서비스는 exe 옆 `service.log`(데스크톱 앱 로그 탭), 드라이버는 `smartthings edge:drivers:logcat <driverId> --hub-address <허브IP>`.

## 개발

`src/`는 Lua 5.3 모듈(`handlers/` · `device/` · `model/`), `profiles/`는 프로필, `capabilities/`는 커스텀 capability 정의 · 프레젠테이션 · 번역, `tests/`는 테스트다.
설계와 모듈 지도는 [`../docs/design/edge-driver.md`](../docs/design/edge-driver.md), 플랫폼 함정은 [`../docs/design/edge-platform-notes.md`](../docs/design/edge-platform-notes.md)에 있다.

```bash
cd edge
npm install && npm test                 # fengari로 Lua 5.3 테스트 (bun tools/lua.js tests/run.lua 도 같다)
node tools/lua.js tests/syntax.lua      # 전 모듈 컴파일
npm run test-build                      # 주석 뗀 패키지 트리로 같은 테스트
node tools/gen-profiles.js --check      # 생성된 프로필이 템플릿과 같은지
node tools/build.js && smartthings edge:drivers:package build/edge
smartthings edge:channels:assign <driverId> <version> --channel <channelId>
```

- **프로필은 손으로 쓰지 않는다.** 현재 프로필 스무 개는 `tools/profile-template.yml`에서 `tools/gen-profiles.js`가 만든다. 화면을 바꾸면 프로필 버전을 올린다(설계 §6.6).
- **정의를 바꾸면 새 capability id가 필요하다.** 허브가 정의를 id 단위로 캐시한다. 계정 작업은 `tools/create-capabilities.sh`(최초 생성)와 `tools/sync-capabilities.sh`(갱신), 다른 계정으로 옮길 때는 `tools/apply-namespace.js`.
- 패키지는 `tools/build.js`가 주석을 빈 줄로 바꿔 만든 `build/edge/`다(커밋하지 않음). 허브 로그의 줄 번호는 `src/`와 같다.
- CI가 `edge-vX.Y.Z` 태그에서 같은 일을 한다. 태그는 `src/driver_version.lua`와 일치해야 한다.

---

## English summary

A purpose-built SmartThings Edge driver for the PC Control service.
It runs locally on the hub and talks to the PC's `/st/v1` API (service **v1.1.0+**; the driver 1.1 features need **v1.2.0**).

- Real power state, visible grace periods and schedules, Wake-on-LAN through the adapter the PC picks.
- SSDP discovery fills in address, port and hostname; only the secret has to be typed.
- Driver 1.1: media controls, presets, "send a message to the PC", keep awake, laptop battery, and one child device per watched app (running/stopped) for routines.
- Failures are named: secret mismatch, unreachable, incompatible version, WoL not ready.

**Install** — enroll in the channel (link above), install the driver, then *Add device → Scan nearby* with the PC and PC Control running; the scan is the only way to add a device.
Enter the secret in the device settings.
If nothing is found, check the discovery status on the Windows app's SmartThings tab.

**Develop** — `npm test` runs the Lua 5.3 suite under fengari; `node tools/build.js && smartthings edge:drivers:package build/edge` packages it.
A presentation change needs a new profile name; a definition change needs a new capability id.
