package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Discord DMs the owner (DISCORD_USER_ID) each proposal with Approve/Reject buttons;
// only that user can press them. Buttons that aren't this app's ("pm:...") are
// ignored, so the bot token can be shared with another bot that does the same.
type Discord struct {
	dg     *discordgo.Session
	user   string
	dm     string
	webURL string
	engine *Engine
}

func NewDiscord(token, user, webURL string) (*Discord, error) {
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	dg.Identify.Intents = discordgo.IntentsNone
	d := &Discord{dg: dg, user: user, webURL: webURL}
	dg.AddHandler(d.onInteraction)
	if err := dg.Open(); err != nil {
		return nil, err
	}
	ch, err := dg.UserChannelCreate(user)
	if err != nil {
		return nil, fmt.Errorf("open DM: %w", err)
	}
	d.dm = ch.ID
	return d, nil
}

func money(v float64) string { return "$" + strconv.FormatFloat(v, 'f', 2, 64) }

func (d *Discord) render(p *Proposal) (string, []discordgo.MessageComponent) {
	var b strings.Builder
	title := fmt.Sprintf("**Pelosi mirror: proposal #%d**", p.ID)
	if p.DryRun {
		title += " (dry run: preview only)"
	}
	fmt.Fprintf(&b, "%s\n%s\nAccount value %s\n\n", title, p.Reason, money(p.Total))
	for _, o := range p.Orders {
		mark := "•"
		switch o.Status {
		case "placed", "previewed":
			mark = "✅"
		case "failed":
			mark = "❌"
		case "skipped":
			mark = "⏭️"
		}
		fmt.Fprintf(&b, "%s %s  (%s → %s)", mark, o, money(o.From), money(o.To))
		if o.Result != "" {
			fmt.Fprintf(&b, " — %s", o.Result)
		}
		b.WriteString("\n")
	}
	switch p.Status {
	case "pending":
		b.WriteString("\nApprove to place these orders in Fidelity.")
	default:
		fmt.Fprintf(&b, "\nStatus: **%s**", p.Status)
		for _, l := range p.Log {
			b.WriteString("\n" + l)
		}
	}
	s := b.String()
	if len(s) > 1900 {
		s = s[:1900] + "\n… (full list on the web page)"
	}
	row := discordgo.ActionsRow{}
	if p.Status == "pending" {
		id := strconv.Itoa(p.ID)
		row.Components = append(row.Components,
			discordgo.Button{Label: "Approve", Style: discordgo.SuccessButton, CustomID: "pm:approve:" + id},
			discordgo.Button{Label: "Reject", Style: discordgo.DangerButton, CustomID: "pm:reject:" + id})
	}
	if strings.HasPrefix(d.webURL, "http") {
		row.Components = append(row.Components, discordgo.Button{Label: "Details", Style: discordgo.LinkButton, URL: d.webURL})
	}
	if len(row.Components) == 0 {
		return s, []discordgo.MessageComponent{}
	}
	return s, []discordgo.MessageComponent{row}
}

func (d *Discord) Proposal(p *Proposal) string {
	text, comps := d.render(p)
	m, err := d.dg.ChannelMessageSendComplex(d.dm, &discordgo.MessageSend{Content: text, Components: comps})
	if err != nil {
		log.Printf("discord send: %v", err)
		return ""
	}
	return m.ID
}

func (d *Discord) Update(p *Proposal) {
	if p.MsgID == "" {
		return
	}
	text, comps := d.render(p)
	if _, err := d.dg.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: p.MsgID, Channel: d.dm, Content: &text, Components: &comps}); err != nil {
		log.Printf("discord edit: %v", err)
	}
}

func (d *Discord) Say(text string) {
	if _, err := d.dg.ChannelMessageSend(d.dm, text); err != nil {
		log.Printf("discord say: %v", err)
	}
}

func (d *Discord) onInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionMessageComponent {
		return
	}
	parts := strings.Split(i.MessageComponentData().CustomID, ":")
	if len(parts) != 3 || parts[0] != "pm" {
		return // not ours (another bot may share the token)
	}
	uid := ""
	if i.User != nil {
		uid = i.User.ID
	} else if i.Member != nil {
		uid = i.Member.User.ID
	}
	reply := func(msg string) {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: msg, Flags: discordgo.MessageFlagsEphemeral}})
	}
	if uid != d.user {
		reply("Only the account owner can approve trades.")
		return
	}
	id, _ := strconv.Atoi(parts[2])
	msg, err := d.engine.Decide(id, parts[1] == "approve", "Discord")
	if err != nil {
		msg = err.Error()
	}
	reply(msg)
}

// logNotifier is used when Discord isn't configured.
type logNotifier struct{}

func (logNotifier) Proposal(p *Proposal) string {
	log.Printf("proposal #%d: %d orders", p.ID, len(p.Orders))
	return ""
}
func (logNotifier) Update(p *Proposal) { log.Printf("proposal #%d: %s", p.ID, p.Status) }
func (logNotifier) Say(t string)       { log.Print(t) }
