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
    "wake_failed", "wol_not_ready", "unauthorized", "unreachable",
    "incompatible_service", "incompatible_driver", "no_secret", "badrequest",
  }
  for _, key in ipairs(required) do
    h.assert_true(i18n.has(key), "missing string " .. key)
  end
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
