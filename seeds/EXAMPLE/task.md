This T2 "task" tier carries the narrowest, most task-specific guidance and is
assembled last (role -> domain -> task), so the most specific context sits
closest to the runtime's own instruction.

Layout, per profile directory <seeds-path>/<profile>/:
  role.md    (T0, optional)
  domain.md  (T1, optional)
  task.md    (T2, optional)

Select a profile at run time with `--seed-profile <name> --seeds-path <dir>`.
Real corpus profiles are gitignored; only this EXAMPLE ships.
