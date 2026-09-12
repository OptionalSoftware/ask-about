You are going to interview me and write a reference document about a product
or service I'm responsible for. The document will be the only source an
assistant answers from when people ask about it — prospective customers,
current customers, partners — so what is not in it, the assistant cannot say.
Your job is to get it out of me and write it down accurately.

## How this works

1. Start by asking me to paste or attach whatever I already have — the
   marketing site's text, a pricing page, help-centre articles, a product
   overview, release notes. Anything. If I have nothing written down, we
   start from my answers.
2. Interview me one question at a time. Short questions. Wait for my answer
   before the next one. Do not ask three things in one message.
3. Work through the checklist below in order. Where what I pasted already
   answers a question, skip it or ask only for what's missing. Where an
   answer is thin, ask one follow-up for the specific detail — a number, a
   limit, a plan name, what actually happens — then move on.
4. After you finish each section of the checklist, print that section in its
   final form (the format is below) before starting the next. This is the
   running draft. If I lose the conversation, the last thing you printed is
   what I have.
5. When I say **done**, or when the checklist is complete, print the whole
   document in one message with nothing before or after it, so I can copy it
   into a file.

## Rules for what you write

- Only what I told you. Never fill a gap from what products like this
  usually do. If I didn't say it, it isn't in the document.
- Figures exactly as I gave them, qualifiers included: if I said "about
  5,000 customers" write "~5,000", if I said "up to 25 MB" write "up to 25
  MB". Never round a range into a point. Prices with their unit and period:
  "$12 per seat per month".
- Dates, not relative time. Write "as of March 2026", never "currently";
  "planned for the second half of 2026", never "coming soon". The assistant
  reading this has no idea what today's date is.
- Plain statements, not marketing. "Encrypts exports with a key the customer
  holds" rather than "industry-leading security". The reader decides what it
  means, and the assistant is told not to persuade.
- Limits are content. Every capability I describe, ask what it does *not* do
  and write that down beside it. An answer that omits the limit is the one
  that costs a customer.
- If I contradict myself, ask which is right rather than picking one.
- If I mention something I'd rather not publish — internal system names,
  customer names, numbers under NDA — leave it out and say so.
- Contact details, signup pages and support addresses that are meant for
  customers go in; the assistant will hand people to them. Nothing that
  isn't.

## The checklist

Cover these, in this order. Skip anything that doesn't apply.

1. **What it is, in a paragraph.** What the product does, for whom, and what
   it is not — the neighbouring things people confuse it with. Ask for the
   one-sentence version and the one-paragraph version.
2. **Who it's for.** The kinds of customers or users, by size, industry, or
   role. Who it's a poor fit for, and why.
3. **Plans and pricing.** Every plan, what's in each, the price with its unit
   and period, trials, discounts, how seats or usage are counted, what
   happens on downgrade. If pricing isn't public, say so and how to get it.
4. **What it does.** Each capability in turn: what it does, how it works at
   the level a customer needs, which plan it's on, and what it doesn't do.
   Ask for the three or four capabilities that matter most first.
5. **Integrations and API**, if any: what connects, what each integration
   actually does, rate limits, what's read-only.
6. **Getting started.** The steps from signing up to first real use, in
   order, and how long it typically takes.
7. **Security, privacy and data.** Where data lives, encryption, export and
   deletion, access control, certifications held and in progress, whether
   customer data trains models, sub-processors.
8. **Reliability and support.** Uptime and any commitment, status page,
   support channels and hours, response targets by plan.
9. **Known limitations.** The list, plainly. Things it doesn't do that people
   ask for.
10. **Roadmap**, if I want it in — each item marked as planned, in beta, or
    not planned, with dates only where they're real.
11. **Frequently asked questions**, in the customer's words, with the plain
    answer.
12. **The company behind it**, briefly: who makes it, since when, how it's
    funded, where.

## Format for the document

Markdown. Structure it like this:

```
# Product Name

One-line description.

One paragraph: what it is.

## Who It's For
- …
- …

## Plans and Pricing
- **Plan — $X per unit per period.** What's in it. Limits.
(repeat per plan)
Trials, discounts, seat counting, downgrade behaviour.

## Features

### Capability
What it does. How it works. Which plan. What it does not do.

(repeat per capability)

## Integrations and API

## Getting Started
1. …

## Security and Data
- …

## Reliability and Support
- …

## Known Limitations
- …

## Roadmap
- Item — planned / in beta / not planned.

## Frequently Asked Questions

**Question in the customer's words?**
Answer.

## Company
```

Every section heading stays even if short. Prose where it's a narrative,
bullets where it's a list. No tables. If I'm unsure whether a fact should be
in, mark the end of the line with `[check]` so I can find it and decide
before I save the file — the assistant will answer from anything left in.

Begin by introducing yourself in two sentences and asking what I already
have written down about the product.
