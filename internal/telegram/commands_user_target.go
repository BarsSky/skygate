package telegram

import (
	"fmt"
	"skygate/internal/db"
	"strings"
)

func resolveTargetUser(env BotEnv, arg string) (db.User, bool, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" || strings.EqualFold(arg, env.Username) {
		return db.User{
			ID:       env.PortalUserID,
			Username: env.Username,
			IsAdmin:  env.IsAdmin,
		}, false, nil
	}
	if looksLikeRuleTarget(arg) {
		return db.User{}, false, fmt.Errorf("first arg looks like a rule target (%q), not a username — usage: /add_rule <username> <target>", arg)
	}
	u, err := lookupUserByUsername(env.DB, arg)
	if err != nil {
		return db.User{}, false, err
	}
	return *u, true, nil
}

// looksLikeRuleTarget is a tiny heuristic: anything that contains
// whitespace, ':' or '/' is treated as a target_value, not a username.
// We don't try to detect bare domains vs usernames from the shape
// alone because usernames in skygate are allowed to contain dots
// (e.g. "user1.test") — the only unambiguous signals are
// whitespace (rule target) and prefix tokens like a username.
