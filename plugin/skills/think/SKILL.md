---
name: think
description: Sharpen the user's thinking on a topic by testing it against their highlights and notes, recommend what to read next, and write a position note.
argument-hint: [topic]
disable-model-invocation: true
---

The user names a topic. With no topic, pick a random tag that spans three or more books, and say which one you picked:

```sh
cmpl tags | jq -r '.tags[] | select(.books >= 3) | "\(.id)\t\(.name)"' | awk 'BEGIN { srand() } { a[NR] = $0 } END { print a[int(rand() * NR) + 1] }'
```

Use its name as the topic and its id in step 1.

Help the user reach a position that is **precise**, **coherent**, and held with honest **credence**. Reason like a Bayesian: every claim is a hypothesis, every highlight is evidence that moves it by some amount, and the user decides. Treat the library as one source among several: bring in the strongest relevant ideas from outside it too, labeled as outside the library.

Run every library read through `cmpl` (read-only; `cmpl help <command>` has flags and output). Cite each highlight by id (#412); the web app shows it at `/h/412` on the port `cmpl serve` uses (default 8420).

## 1. Gather

1. Run `cmpl tags` and note every tag that bears on the topic, including neighboring ones.
2. Run `cmpl search '<phrasing>' --notes --limit 20` for the topic in four or more phrasings: the plain topic, the underlying question, its opposite or failure mode, and an adjacent field's framing of it. Run `cmpl list --tag <id> --notes` for each matching tag.
3. For the strongest hits (favorites, highlights with user notes, highlights that state a claim), run `cmpl similar <id> --notes`.

Done when a new phrasing or `similar` call returns mostly ids you already have, and you hold evidence on both sides of the topic or know that one side is missing from the library.

## 2. Map

Show the user a map of the evidence, in this order:

- **Hypotheses**: the distinct claims the highlights make, each stated precisely enough to be wrong, with its ids and books, and how strongly the evidence supports it.
- **Tensions**: highlights that pull against each other. Quote the conflicting lines, and name the crux: the fact or value that would settle it.
- **The user's voice**: what their own notes (source `user`) and favorites say, and where they agree or disagree with the books or with each other.
- **Gaps**: the strongest outside counterarguments, angles the library leaves out, and claims resting on a single book or a vivid anecdote.

Keep quotes short. Every claim from the library carries an id.

## 3. Question

Ask one question at a time, each grounded in a specific tension, gap, or note, citing its ids. Challenge the user's answers:

- **Vague**: ask what the claim predicts, what would show it wrong, and how it differs from its opposite. Restate it in sharper words and ask if that is what they mean.
- **Incoherent**: when two of their claims conflict, or an answer conflicts with something they highlighted or noted, quote both and ask which gives way.
- **Overconfident or underconfident**: ask for a credence and what moved it there; point to evidence that should move it further.
- **One-sided**: steelman the view they reject, and ask what they would expect to see if it were true.

Bring in new evidence with `cmpl` when an answer opens a new angle.

Done when the user has stated a position in their own words that is specific enough to be wrong, consistent with their other stated claims, and carries a credence; and each tension from the map is either resolved or the user has chosen to leave it open.

## 4. Recommend

Recommend three to five books or essays, each aimed at one specific weak point from the session: a crux left open, a gap, a claim resting on a single book, or the strongest view the user rejected. Favor works that could move the user's credence over works that confirm it. For each, give the title and author, the crux or gap it targets (with ids), and how reading it could change the position.

Check each one against `cmpl books` and drop any the user has already read. Confirm each title and author exist before recommending them; search the web when unsure.

Done when every recommendation names the weak point it targets, and the user has had a chance to swap one out or ask for more on a specific area.

## 5. Write

Write the note where the user's instructions say they keep notes; if they say nothing, ask. Use the user's words for the position, not yours:

```markdown
# <Topic>

YYYY-MM-DD, from commonplace highlights

## Position
<the user's position, in their words, with credence>

## What would change it
- <evidence that would raise or lower the credence>

## Evidence
- <claim> — "<short quote>" (<book>, #412)

## Open questions
- <tension left open, with its crux and ids>

## Read next
- <title>, <author> — <the crux or gap it targets, and how it could change the position>
```

Show the user the path to the note.
