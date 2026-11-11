// Package intents enumerates the QQ gateway event subscriptions.
package intents

// Group is one intent bit and the events it delivers.
type Group struct {
	Bit     uint   `json:"bit"`
	Name    string `json:"name"`
	Label   string `json:"label"`
	Events  string `json:"events"`
	Default bool   `json:"default"`
	// PrivateOnly marks guild intents that only 私域 bots may subscribe to;
	// subscribing without permission makes the gateway reject the connection.
	PrivateOnly bool `json:"private_only"`
}

// All lists every intent the platform documents, in bit order.
var All = []Group{
	{Bit: 0, Name: "GUILDS", Label: "频道与子频道", Events: "GUILD_CREATE / UPDATE / DELETE、CHANNEL_CREATE / UPDATE / DELETE"},
	{Bit: 1, Name: "GUILD_MEMBERS", Label: "频道成员", Events: "GUILD_MEMBER_ADD / UPDATE / REMOVE", PrivateOnly: true},
	{Bit: 9, Name: "GUILD_MESSAGES", Label: "频道私域消息", Events: "MESSAGE_CREATE、MESSAGE_DELETE", PrivateOnly: true},
	{Bit: 10, Name: "GUILD_MESSAGE_REACTIONS", Label: "频道表情表态", Events: "MESSAGE_REACTION_ADD / REMOVE", PrivateOnly: true},
	{Bit: 12, Name: "DIRECT_MESSAGE", Label: "频道私信", Events: "DIRECT_MESSAGE_CREATE / DELETE", PrivateOnly: true},
	{Bit: 25, Name: "GROUP_AND_C2C_EVENT", Label: "群与单聊消息", Events: "群 @ 消息、单聊消息、入群/退群、好友与主动消息开关", Default: true},
	{Bit: 26, Name: "INTERACTION", Label: "互动事件", Events: "INTERACTION_CREATE"},
	{Bit: 27, Name: "MESSAGE_AUDIT", Label: "消息审核", Events: "MESSAGE_AUDIT_PASS / REJECT"},
	{Bit: 28, Name: "FORUMS_EVENT", Label: "论坛事件", Events: "FORUM_THREAD / POST / REPLY 系列", PrivateOnly: true},
	{Bit: 29, Name: "AUDIO_ACTION", Label: "音频动作", Events: "AUDIO_START / FINISH / ON_MIC / OFF_MIC", PrivateOnly: true},
	{Bit: 30, Name: "PUBLIC_GUILD_MESSAGES", Label: "频道全量消息（@机器人）", Events: "AT_MESSAGE_CREATE、PUBLIC_MESSAGE_DELETE"},
}

// Mask turns selected bit numbers into the int64 sent to the gateway.
func Mask(bits []uint) int64 {
	var m int64
	for _, b := range bits {
		if b < 63 {
			m |= 1 << b
		}
	}
	return m
}

// Bits expands a mask back into a bit-number list, for editing in the console.
func Bits(mask int64) []uint {
	var out []uint
	for _, g := range All {
		if mask&(1<<g.Bit) != 0 {
			out = append(out, g.Bit)
		}
	}
	return out
}

// DefaultMask is the value new bots start with.
func DefaultMask() int64 {
	var m int64
	for _, g := range All {
		if g.Default {
			m |= 1 << g.Bit
		}
	}
	return m
}

// UnknownBits reports bits set in mask that this table does not describe, so
// the console can warn about values it cannot render as checkboxes.
func UnknownBits(mask int64) uint64 {
	known := int64(0)
	for _, g := range All {
		known |= 1 << g.Bit
	}
	return uint64(mask &^ known)
}
