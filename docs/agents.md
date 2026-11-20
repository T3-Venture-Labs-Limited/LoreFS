# Setting up LoreFS for an agent

The drive carries a file for agents at its top level, `AGENTS.md` (the
"rules file"), written when the drive is created. It tells an agent what the
drive is for, how to keep folders shallow, how to use version history, trash
and sandboxes, and what not to do. Point your agent at it, or paste what
`lorefs agents-snippet` prints into the agent's own instructions.

What an agent needs to know:

- The drive is an ordinary folder (`~/Lore` by default). Read and write files
  as usual.
- Saves are versioned. `lorefs history <file> --json` lists versions;
  `lorefs restore <file> <n>` undoes a change; nothing a person or an agent
  overwrites is lost for 30 days.
- To change many files, work in a sandbox: `lorefs sandbox create <folder>
  [name] --json` makes a private copy that costs no storage; edit it freely;
  `lorefs sandbox diff <name>` shows the changes; `lorefs sandbox apply
  <name> --yes` brings them back.
- Two writers to one file do not merge. Write complete files, prefer one topic
  per file, and use exclusive create to claim work.
- Containers can use the drive after `lorefs containers on` (one administrator
  step is explained when needed).
- `lorefs status --json` and `lorefs history --json` give structured output
  for programs.

The rules file belongs to the drive: edit it to suit your team. A newer
template is mentioned by `lorefs status` without changing your file;
`lorefs rules diff` shows the differences.
