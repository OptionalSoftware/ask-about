# Larkspur Desk

Shared inbox and help desk for small support teams.

FICTIONAL SAMPLE: Larkspur Desk does not exist. Every company, customer, price,
date, and figure in this document is invented to demonstrate a product corpus.

*This is a long-form reference document. It is intentionally exhaustive so it
can serve as source material for a Q&A assistant covering the whole product.
Entries marked **[public]** appear on the marketing site; the rest come from
the internal knowledge base and support macros. Pricing is as of the March 2026
plan change.*

## What it is

Larkspur Desk turns a shared email address — support@, hello@, billing@ — into a
queue a team can work from. Every incoming message becomes a conversation with
an owner, a status, and a history, and replies go out from the shared address
rather than from whoever happened to answer. [public]

It is built for teams of two to twenty people who answer customer email and
have outgrown a single inbox but do not want a full enterprise ticketing
system. [public]

It is not a live-chat product, a CRM, or a knowledge base. It integrates with
those; it does not replace them.

## Who it is for

- Support teams at software companies with under about 5,000 customers.
- Operations and order-support desks at small online retailers.
- Agencies and consultancies that answer client email from a shared address.
- Internal help desks (IT, HR, facilities) at companies under ~300 staff.

Teams above twenty seats can use it, but the assignment and reporting features
were designed around a queue one person can read end to end each morning.

## Plans and pricing [public]

All prices are per seat, per month, billed monthly. Annual billing is 20% less.

- **Starter — $12 per seat.** One shared inbox. Assignment, statuses, private
  notes, canned replies, basic search. Up to 5 seats. 90 days of conversation
  history.
- **Team — $29 per seat.** Unlimited shared inboxes. Everything in Starter
  plus rules, tags, SLA timers, saved views, reporting, the API, and every
  integration. Unlimited history.
- **Business — $49 per seat.** Everything in Team plus SSO (SAML), audit log,
  data residency choice (US or EU), custom roles, and a 99.9% uptime
  commitment with service credits.

A 14-day trial of Team needs no card. Downgrading from Team to Starter keeps
conversations but hides those older than 90 days until the plan is upgraded
again; nothing is deleted.

There is no free plan. Non-profits get 30% off any plan on request.

Seats are billed for the peak number of active users in the billing period.
Deactivating a user frees the seat immediately; their conversations stay.

## Core features

### Conversations

Every inbound email becomes a conversation. Replies from the customer thread
onto it by message headers first and by subject line as a fallback, so a reply
with a changed subject still lands in the right place about 97% of the time.

Each conversation has one owner or none, a status (Open, Waiting, Done), any
number of tags, and private notes that the customer never sees. Notes support
@-mentions, which notify the mentioned teammate.

Two people opening the same conversation see a "Dana is viewing" banner.
Replies are not locked; if two replies are sent within 60 seconds of each
other, the second sender gets a warning before it goes out, not after.

### Assignment and rules

Conversations can be assigned by hand, by round-robin across a group, or by
rules. Rules match on sender domain, subject, recipient address, tags, or
keywords in the body, and can assign, tag, set status, set priority, or send a
canned reply. Rules run in order and stop at the first match by default; a rule
can be marked "continue" to let later rules run too.

Round-robin skips teammates who are marked away. It does not balance by
workload — it is strictly alternating.

### Canned replies and variables

Canned replies are shared across the workspace and support variables:
customer first name, customer email, conversation number, assignee name, and
any tag. A canned reply that references a variable with no value inserts
nothing rather than the variable name.

### SLA timers (Team and above)

A first-response target and a resolution target can be set per inbox and
overridden per tag. Timers count business hours only, using the workspace's
schedule and holiday list. A breached timer marks the conversation and can
trigger a rule. There is no automatic escalation to another person; a rule has
to do that.

### Saved views and reporting (Team and above)

Any filter can be saved as a view and shared. Reporting covers volume by day,
first-response and resolution times as medians and 90th percentiles, breaches,
and per-teammate reply counts. Reports can be exported as CSV. There is no
custom report builder; the eight standard reports are what there is.

### Search

Search covers subject, body, sender, notes, and tags across the conversations
the plan's history allows. It is full-text with exact-phrase quoting. Search
does not index attachments.

### Collision-safe sending

Outbound mail is sent through Larkspur Desk's own servers and signed with DKIM
for the customer's domain once DNS is set up. Until DNS is verified, mail goes
out with a "via larkspurdesk" sender, which some corporate filters flag; the
onboarding checklist calls this out as the first thing to fix.

## Integrations (Team and above)

- **Slack:** new conversations, assignments, mentions, and SLA breaches post
  to a chosen channel. Replying from Slack is not supported.
- **Shopify and WooCommerce:** the customer's recent orders appear in a side
  panel matched by email address. Read-only.
- **Stripe:** subscription status and last three invoices in the side panel.
  Read-only.
- **Zapier:** triggers for new conversation, status change, and tag added;
  an action for creating a conversation.
- **Webhooks:** the same events as Zapier, delivered as signed JSON with three
  retries over 15 minutes.
- **API:** REST, key-based. Rate limit 600 requests per minute per workspace.
  Covers conversations, messages, tags, users, and reports. There is no
  GraphQL API.

Integrations are configured per workspace by an admin. Each integration has
its own on/off switch and can be removed without affecting the others.

## Setup

1. Create a workspace and invite teammates by email. The first user is the
   owner; the owner can name other admins.
2. Add a shared inbox by forwarding the address (support@yourdomain) to the
   inbox's unique Larkspur Desk address. Forwarding guides exist for Google
   Workspace, Microsoft 365, Fastmail, and generic IMAP.
3. Add the two DNS records the setup page shows (SPF include and a DKIM
   CNAME) so replies send from your domain. Verification runs every ten
   minutes; most domains verify within an hour.
4. Import history, optionally: a .mbox or .eml archive up to 2 GB per upload,
   or a CSV of past tickets with a documented column layout. Imported
   conversations are marked Done and are searchable.
5. Set business hours and, on Team, SLA targets.

Most teams are answering from Larkspur Desk within the same working day. The
median time from signup to first reply sent is 41 minutes across trials that
convert.

## Security and data

- All data is encrypted in transit (TLS 1.2 or later) and at rest.
- Workspace data is stored in the US by default. Business plan workspaces can
  choose EU residency at creation; it cannot be changed afterwards without a
  migration handled by support.
- Exports: any admin can export all conversations as JSON or .mbox at any
  time. Exports are encrypted with a key only the requesting admin holds; Relay
  Desk cannot open an export after it is generated.
- Deletion: a deleted workspace is unrecoverable after 30 days. Individual
  conversations can be deleted by admins and are purged from backups within
  60 days.
- Access: SSO via SAML on Business. Two-factor authentication (TOTP) on every
  plan, and an admin can require it for the workspace.
- Audit log (Business) records logins, role changes, exports, deletions, and
  integration changes for 12 months.
- Sub-processors are listed on the trust page. There are four: the hosting
  provider, the email delivery provider, the error-tracking service, and the
  payment processor.

Larkspur Desk has not completed a SOC 2 audit. A Type I is in progress with an
expected completion in the second half of 2026.

## Reliability and support

- Uptime over the trailing twelve months: 99.94%. The 99.9% commitment applies
  to the Business plan only and is backed by service credits of 10% of the
  month's fee per hour of downtime beyond it, capped at 50%.
- Status page: public, with incident history and a subscription option.
- Support: email, answered by the Larkspur Desk team from their own Larkspur Desk
  workspace. Target first response is one business day on Starter, four
  business hours on Team, and one business hour on Business. Business
  customers also get a shared Slack channel.
- Support hours: 08:00–18:00 US Eastern, Monday to Friday, excluding US
  federal holidays. There is no phone support.

## Known limitations

- No live chat, no chat widget.
- No mobile app. The web app works on phones but was not designed for them;
  triage is reasonable, long replies are not.
- No shared calendar or scheduling.
- No automatic translation. Conversations in any language are stored and
  searched as-is.
- Attachments are limited to 25 MB per message and are not searchable.
- Round-robin does not consider workload.
- Reporting has no custom builder and no scheduled email delivery.
- One workspace per billing account. A company with two brands needs two
  workspaces or a shared one with two inboxes.

## Roadmap markers

These describe intent as of the document date and are not commitments:

- Scheduled report delivery by email: in beta with 30 workspaces.
- Reply from Slack: in design. Not scheduled.
- Attachment search: planned for after the SOC 2 audit.
- Mobile app: not planned.

## Frequently asked questions

**Can two people reply to the same customer by accident?**
Yes, but not silently. A second reply within 60 seconds of the first triggers
a warning before sending, and the "is viewing" banner shows when someone else
has the conversation open. Nothing prevents two deliberate replies.

**What happens to our email if Larkspur Desk goes down?**
Inbound mail is received by your own mail provider and forwarded, so it queues
at your provider and is delivered when Larkspur Desk is reachable again. Nothing
is lost; it is delayed. Outbound replies cannot be sent during an outage.

**Can we bring our own domain for sending?**
Yes, on every plan, via the SPF and DKIM records in setup. Until then mail
sends "via larkspurdesk".

**Is there a free plan?**
No. Starter is the lowest plan, and there is a 14-day Team trial with no card.

**Do you train AI models on our data?**
No. Customer data is not used to train models. The optional reply-suggestion
feature (in beta, Team and above) sends the current conversation to a
third-party model provider under a no-retention agreement and is off unless
an admin turns it on.

**Can we export everything and leave?**
Yes, at any time, as JSON or .mbox, from the workspace settings. Exports are
encrypted with a key the requesting admin holds.

## Company

Larkspur Desk is made by Larkspur Desk Inc. (fictional), founded in 2022, based in
Portland, Oregon, with 14 staff. It is self-funded and has taken no outside
investment.

End of fictional product dossier
