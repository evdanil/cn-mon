//go:build windows

package probe

import (
	"errors"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

func nativePermissionError(err error) bool {
	return errors.Is(err, windows.WSAEACCES) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

func nativePrivilegeFacts() string {
	token := windows.GetCurrentProcessToken()
	user, userErr := token.GetTokenUser()
	groups, groupErr := token.GetTokenGroups()
	if userErr != nil || groupErr != nil {
		return "token_query_error=" + errors.Join(userErr, groupErr).Error()
	}
	groupIDs := make([]string, 0, groups.GroupCount)
	adminEnabled := false
	for _, group := range groups.AllGroups() {
		groupIDs = append(groupIDs, group.Sid.String())
		if group.Sid.String() == "S-1-5-32-544" && group.Attributes&windows.SE_GROUP_ENABLED != 0 {
			adminEnabled = true
		}
	}
	return "user_sid=" + user.User.Sid.String() + " elevated=" + strings.ToLower(strconv.FormatBool(token.IsElevated())) + " admin_group_enabled=" + strings.ToLower(strconv.FormatBool(adminEnabled)) + " groups=" + strings.Join(groupIDs, ",")
}

func nativeCanRaw() bool {
	token := windows.GetCurrentProcessToken()
	groups, err := token.GetTokenGroups()
	if err != nil {
		return false
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.String() == "S-1-5-32-544" && group.Attributes&windows.SE_GROUP_ENABLED != 0 {
			return true
		}
	}
	return false
}

// Windows has no Linux-style ambient/permitted CAP_NET_RAW capability sets.
func nativeRawGrantPresent() bool { return false }
