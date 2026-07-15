// 2026-07-15: v0.14.0 — /sync_nodes bot command.
//
// Re-populates node_owner_map from headscale's authoritative
// view. Same DB call as the "Sync from headscale" button on
// /admin/devices; this is the bot-side equivalent for ops
// that live in Telegram.
//
// Why this exists: a node tagged in headscale directly
// (not via skygate's PostAdminNodeTag) has no row in
// node_owner_map. The bot's /exit_nodes (admin) and
// /myexitnodes (user) both read from node_owner_map and
// would report "no nodes found" until the cache is
// rebuilt. /sync_nodes forces the rebuild on demand.

package telegram

import (
	"strconv"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// syncNodesReply is admin-only. Returns a short text reply
// with the inserted/updated counts. The actual headscale
// call + DB upsert is the same as
// App.PostAdminDevicesSyncFromHeadscale, but going through
// the bot means the operator can fire it from a phone
// without bringing up the web UI.
func syncNodesReply(env BotEnv) string {
	lang := env.Lang
	if !env.IsAdmin {
		return i18n.Tf(lang, "bot.admin_only_command", "/sync_nodes")
	}
	if env.HS == nil {
		return i18n.T(lang, "bot.sync_nodes.hs_unavailable")
	}
	nodes, err := env.HS.ListAllNodes()
	if err != nil {
		return i18n.Tf(lang, "bot.sync_nodes.hs_failed", err)
	}
	var syncInfos []db.SyncNodeInfo
	for _, n := range nodes {
		tag := ""
		for _, t := range n.Tags {
			if t != "" {
				tag = t
				break
			}
		}
		var hsUID int64
		if n.UserID != "" {
			if v, perr := strconv.ParseInt(n.UserID, 10, 64); perr == nil {
				hsUID = v
			}
		}
		syncInfos = append(syncInfos, db.SyncNodeInfo{
			ID:       n.ID,
			Hostname: n.Hostname,
			Tag:      tag,
			Username: n.UserName,
			HSUserID: hsUID,
			// TaggedBy=0: the bot path doesn't have a
			// skygate user context. The admin button
			// path uses a non-zero TaggedBy.
			TaggedBy: 0,
		})
	}
	ins, upd, err := db.SyncNodesFromHeadscale(env.DB, syncInfos)
	if err != nil {
		return i18n.Tf(lang, "bot.sync_nodes.db_failed", err)
	}
	return i18n.Tf(lang, "bot.sync_nodes.ok", ins, upd, len(syncInfos))
}
