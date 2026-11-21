package webui

// Tests for the administrator check of the local login (#168): which
// tokens and group memberships count as BUILTIN\Administrators.

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// fakeToken is a tokenReader with fixed answers.
type fakeToken struct {
	grp       []tokenGroup
	grpErr    error
	elev      uint32
	elevErr   error
	linked    []tokenGroup
	linkedErr error
	// linkedAsked records whether the linked token was read.
	linkedAsked *bool
}

func (f fakeToken) groups() ([]tokenGroup, error)  { return f.grp, f.grpErr }
func (f fakeToken) elevationType() (uint32, error) { return f.elev, f.elevErr }
func (f fakeToken) linkedGroups() ([]tokenGroup, error) {
	if f.linkedAsked != nil {
		*f.linkedAsked = true
	}
	return f.linked, f.linkedErr
}

const (
	testUser      = "S-1-5-21-111-222-333-1001"
	testOtherUser = "S-1-5-21-111-222-333-1002"
	builtinAdmin  = "S-1-5-21-111-222-333-500"
	builtinUsers  = "S-1-5-32-545"
	everyoneSID   = "S-1-1-0"
	interactive   = "S-1-5-4"
	authUsers     = "S-1-5-11"
	entraGroup    = "S-1-12-1-1111-2222-3333-4444"
	logonSID      = "S-1-5-5-0-123456"

	enabled  = windows.SE_GROUP_ENABLED | windows.SE_GROUP_ENABLED_BY_DEFAULT | windows.SE_GROUP_MANDATORY
	denyOnly = windows.SE_GROUP_USE_FOR_DENY_ONLY
)

// commonGroups are the groups every interactive user's token has.
func commonGroups(extra ...tokenGroup) []tokenGroup {
	g := []tokenGroup{
		{everyoneSID, enabled}, {builtinUsers, enabled}, {interactive, enabled},
		{authUsers, enabled}, {logonSID, enabled | windows.SE_GROUP_LOGON_ID},
	}
	return append(g, extra...)
}

// admins answers the local Administrators group's members.
func admins(sids ...string) func() ([]string, error) {
	return func() ([]string, error) { return sids, nil }
}

func TestCheckAdmin(t *testing.T) {
	adminMembers := admins(builtinAdmin, testUser)
	cases := []struct {
		name    string
		tok     fakeToken
		members func() ([]string, error)
		want    string // Via; "" = not an administrator
	}{
		{
			"elevated administrator (or UAC off)",
			fakeToken{grp: commonGroups(tokenGroup{administratorsSID, enabled | windows.SE_GROUP_OWNER}), elev: tokenElevationTypeFull},
			adminMembers, adminViaEnabled,
		},
		{
			"UAC-filtered administrator: Administrators deny-only",
			fakeToken{grp: commonGroups(tokenGroup{administratorsSID, denyOnly}), elev: tokenElevationTypeLimited},
			adminMembers, adminViaDenyOnly,
		},
		{
			// A Microsoft account is a local account with a linked identity:
			// same SIDs, same token shape as a local administrator.
			"Microsoft account administrator, filtered token",
			fakeToken{grp: commonGroups(tokenGroup{administratorsSID, denyOnly}, tokenGroup{"S-1-5-64-36", enabled} /* CLOUD_ACCOUNT_AUTHENTICATION */), elev: tokenElevationTypeLimited},
			adminMembers, adminViaDenyOnly,
		},
		{
			"limited token without the SID, elevated linked token has it",
			fakeToken{grp: commonGroups(), elev: tokenElevationTypeLimited, linked: commonGroups(tokenGroup{administratorsSID, enabled | windows.SE_GROUP_OWNER})},
			admins(builtinAdmin), adminViaLinked,
		},
		{
			// Administrator protection: a fully standard token, no linked
			// token, but the user is in the local group.
			"Administrator protection: standard token, user in the group",
			fakeToken{grp: commonGroups(), elev: tokenElevationTypeDefault},
			adminMembers, adminViaMember,
		},
		{
			"Microsoft account admin under Administrator protection",
			fakeToken{grp: commonGroups(tokenGroup{"S-1-5-64-36", enabled}), elev: tokenElevationTypeLimited, linkedErr: windows.ERROR_NO_SUCH_LOGON_SESSION},
			adminMembers, adminViaMember,
		},
		{
			"Entra ID group that is in Administrators",
			fakeToken{grp: commonGroups(tokenGroup{entraGroup, enabled}), elev: tokenElevationTypeDefault},
			admins(builtinAdmin, entraGroup), adminViaNested,
		},
		{
			"standard user",
			fakeToken{grp: commonGroups(), elev: tokenElevationTypeDefault},
			admins(builtinAdmin, testOtherUser), "",
		},
		{
			"standard user, limited token whose linked token is not an admin's",
			fakeToken{grp: commonGroups(), elev: tokenElevationTypeLimited, linked: commonGroups()},
			admins(builtinAdmin), "",
		},
		{
			"only the built-in Administrator is",
			fakeToken{grp: commonGroups(), elev: tokenElevationTypeDefault},
			admins(builtinAdmin), "",
		},
		{
			"Administrators attributes neither enabled nor deny-only",
			fakeToken{grp: commonGroups(tokenGroup{administratorsSID, 0}), elev: tokenElevationTypeDefault},
			admins(builtinAdmin), "",
		},
		{
			"a group of the token in Administrators only when the token holds it",
			fakeToken{grp: commonGroups(tokenGroup{entraGroup, 0}), elev: tokenElevationTypeDefault},
			admins(builtinAdmin, entraGroup), "",
		},
		{
			"group lookup fails: not an administrator",
			fakeToken{grp: commonGroups(), elev: tokenElevationTypeDefault},
			func() ([]string, error) { return nil, windows.ERROR_ACCESS_DENIED }, "",
		},
		{
			"token unreadable, user in the group",
			fakeToken{grpErr: windows.ERROR_ACCESS_DENIED, elevErr: windows.ERROR_ACCESS_DENIED},
			adminMembers, adminViaMember,
		},
	}
	for _, c := range cases {
		v := checkAdmin(c.tok, testUser, c.members)
		if v.Admin != (c.want != "") || v.Via != c.want {
			t.Errorf("%s: admin=%v via %q (%s), want via %q", c.name, v.Admin, v.Via, v.Detail, c.want)
		}
	}
}

// The token's own answer comes first: no linked token, no group lookup.
func TestCheckAdminCheapestFirst(t *testing.T) {
	asked := false
	tok := fakeToken{grp: commonGroups(tokenGroup{administratorsSID, denyOnly}), elev: tokenElevationTypeLimited, linkedAsked: &asked}
	v := checkAdmin(tok, testUser, func() ([]string, error) {
		t.Error("the group was listed although the token had the answer")
		return nil, nil
	})
	if !v.Admin || asked {
		t.Errorf("admin=%v, linked token read=%v", v.Admin, asked)
	}

	// A default token has no linked token to ask.
	asked = false
	tok = fakeToken{grp: commonGroups(), elev: tokenElevationTypeDefault, linkedAsked: &asked}
	checkAdmin(tok, testUser, admins())
	if asked {
		t.Error("linked token read for a default token")
	}
}

// A refusal says what the checks saw, for service.log.
func TestCheckAdminDetail(t *testing.T) {
	v := checkAdmin(fakeToken{grp: commonGroups(), elev: tokenElevationTypeLimited, linkedErr: errors.New("no linked token")}, testUser, admins(builtinAdmin))
	for _, want := range []string{"elevation limited", "linked token: no linked token", "not among 1 Administrators members"} {
		if !strings.Contains(v.Detail, want) {
			t.Errorf("detail %q lacks %q", v.Detail, want)
		}
	}
	v = checkAdmin(fakeToken{grp: commonGroups(), elev: tokenElevationTypeDefault}, testUser, func() ([]string, error) { return nil, errors.New("boom") })
	if !strings.Contains(v.Detail, "Administrators members: boom") {
		t.Errorf("detail %q lacks the lookup error", v.Detail)
	}
}

// The real readers on this test process: the verdict agrees with the
// token, and the local group can be listed.
func TestCheckAdminOwnToken(t *testing.T) {
	members, err := localAdministrators()
	if err != nil {
		t.Fatalf("localAdministrators: %v", err)
	}
	if len(members) == 0 {
		t.Error("the local Administrators group listed no members")
	}
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	user := u.User.Sid.String()
	v := checkAdmin(winToken{tok}, user, localAdministrators)
	t.Logf("user %s admin=%v via %q (%s); %d members", user, v.Admin, v.Via, v.Detail, len(members))

	g, err := winToken{tok}.groups()
	if err != nil {
		t.Fatal(err)
	}
	inToken := false
	for _, x := range g {
		if x.SID == administratorsSID && x.Attr&(windows.SE_GROUP_ENABLED|windows.SE_GROUP_USE_FOR_DENY_ONLY) != 0 {
			inToken = true
		}
	}
	if inToken && !v.Admin {
		t.Error("the token lists Administrators but the check said no")
	}
	if _, err := (winToken{tok}).elevationType(); err != nil {
		t.Errorf("elevation type: %v", err)
	}
}
