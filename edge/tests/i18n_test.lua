local h = require "helpers"
local i18n = require "i18n"

local T = {}

function T.test_resolve_defaults_to_english()
  h.assert_equal(i18n.resolve("ko"), "ko")
  h.assert_equal(i18n.resolve("en"), "en")
  -- §6.5: a driver cannot read the hub locale, so auto means English.
  h.assert_equal(i18n.resolve("auto"), "en")
  h.assert_equal(i18n.resolve(nil), "en")
  h.assert_equal(i18n.resolve("fr"), "en")
end

function T.test_translates_both_languages()
  h.assert_equal(i18n.t("ko", "wake_failed"), "깨우기 실패: WoL 응답 없음")
  h.assert_equal(i18n.t("en", "wake_failed"), "Wake failed: no WoL response")
  h.assert_contains(i18n.t("ko", "unreachable"), "연결할 수 없습니다")
  h.assert_contains(i18n.t("en", "unreachable"), "Cannot reach")
end

function T.test_auto_language_falls_back_to_english()
  h.assert_equal(i18n.t("auto", "no_secret"), i18n.t("en", "no_secret"))
end

function T.test_formats_arguments()
  h.assert_equal(i18n.t("ko", "incompatible_service", "1.1.0"), "서비스 v1.1.0 이상 필요")
  h.assert_equal(i18n.t("en", "incompatible_service", "1.1.0"), "Requires service v1.1.0 or newer")
  h.assert_contains(i18n.t("en", "wol_bad_mac", "zz:zz"), "zz:zz")
end

function T.test_unknown_key_returns_the_key()
  h.assert_equal(i18n.t("ko", "no_such_key"), "no_such_key")
  h.assert_false(i18n.has("no_such_key"))
  h.assert_true(i18n.has("wake_failed"))
end

function T.test_all_required_keys_exist()
  local required = {
    "wake_failed", "wol_not_ready", "wol_no_mac", "wol_bad_mac",
    "unauthorized", "forbidden", "unreachable", "ratelimited",
    "incompatible_service", "incompatible_driver", "no_secret", "no_ip",
    "badrequest", "update_available", "update_available_plain",
    "schedule_replaced", "schedule_cancelled", "schedule_none",
    -- #73: discovery, the multi-PC warning and the display child label.
    "discovery_found", "ip_updated", "hostname_mismatch", "display_label",
  }
  for _, key in ipairs(required) do
    h.assert_true(i18n.has(key), "missing string " .. key)
  end
end

function T.test_every_service_command_has_a_display_name()
  -- §4.3: the eight commands the capability offers, plus the ping the driver
  -- uses as a reachability probe. A missing one would show the raw id.
  local commands = {
    "shutdown", "forceshutdown", "restart", "hibernate",
    "suspend", "lock", "turnscreenoff", "turnscreenon", "ping",
  }
  for _, command in ipairs(commands) do
    for _, lang in ipairs({ "ko", "en" }) do
      local label = i18n.command(lang, command)
      h.assert_true(label ~= command and label ~= "",
        string.format("command %s has no %s display name", command, lang))
    end
  end
end

function T.test_the_discovery_strings_carry_their_arguments()
  h.assert_equal(i18n.t("en", "discovery_found", 2), "Found 2 PC(s)")
  h.assert_equal(i18n.t("ko", "discovery_found", 2), "PC 2대를 찾았습니다")
  h.assert_contains(i18n.t("en", "ip_updated", "192.168.1.25"), "192.168.1.25")
  h.assert_contains(i18n.t("ko", "ip_updated", "192.168.1.25"), "192.168.1.25")
  -- §13.1: the warning has to name the other hostname and what to fix.
  h.assert_contains(i18n.t("en", "hostname_mismatch", "LAPTOP-XYZ"), "LAPTOP-XYZ")
  h.assert_contains(i18n.t("en", "hostname_mismatch", "LAPTOP-XYZ"), "MachineGuid")
  h.assert_contains(i18n.t("ko", "hostname_mismatch", "LAPTOP-XYZ"), "MachineGuid")
  h.assert_equal(i18n.t("en", "display_label", "DESKTOP-ABC"), "DESKTOP-ABC Display")
end

function T.test_forbidden_and_unauthorized_say_different_things()
  -- Both end up as `connection = unauthorized` (§4.1), so the message is the
  -- only thing telling the user which of the two to fix.
  h.assert_contains(i18n.t("en", "forbidden"), "allow-list")
  h.assert_contains(i18n.t("en", "unauthorized"), "Secret")
  h.assert_true(i18n.t("ko", "forbidden") ~= i18n.t("ko", "unauthorized"))
end

function T.test_update_available_carries_the_version()
  h.assert_equal(i18n.t("ko", "update_available", "v1.2.0"), "서비스 업데이트 v1.2.0 사용 가능")
  h.assert_equal(i18n.t("en", "update_available", "v1.2.0"), "Service update v1.2.0 available")
end

function T.test_origin_labels()
  h.assert_equal(i18n.origin("ko", "ui"), "앱")
  h.assert_equal(i18n.origin("ko", "remote"), "SmartThings 명령")
  h.assert_equal(i18n.origin("ko", "telegram"), "텔레그램")
  h.assert_equal(i18n.origin("ko", "smartthings"), "SmartThings")
  h.assert_equal(i18n.origin("en", "ui"), "App")
  h.assert_equal(i18n.origin("en", "telegram"), "Telegram")
end

function T.test_unknown_origin_is_passed_through()
  h.assert_equal(i18n.origin("ko", "webhook"), "webhook")
  h.assert_equal(i18n.origin("ko", nil), "")
  h.assert_equal(i18n.origin("ko", ""), "")
end

function T.test_command_labels()
  h.assert_equal(i18n.command("ko", "shutdown"), "종료")
  h.assert_equal(i18n.command("ko", "forceshutdown"), "강제 종료")
  h.assert_equal(i18n.command("ko", "turnscreenon"), "화면 켜기")
  h.assert_equal(i18n.command("en", "shutdown"), "Shut down")
  h.assert_equal(i18n.command("en", "hibernate"), "Hibernate")
  -- A command a newer service knows and this driver does not still shows up.
  h.assert_equal(i18n.command("en", "somethingnew"), "somethingnew")
  h.assert_equal(i18n.command("en", nil), "")
end

return T
