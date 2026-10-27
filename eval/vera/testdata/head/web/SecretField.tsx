import { useState } from "react";

type Props = {
  hasSecret: boolean;
  onReplace: (secret: string) => Promise<void>;
};

// SecretField never shows a stored webhook secret: it says whether one is
// set and accepts a replacement, which it clears once saved.
export function SecretField({ hasSecret, onReplace }: Props) {
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);

  async function save() {
    setSaving(true);
    try {
      await onReplace(draft);
      setDraft("");
    } finally {
      setSaving(false);
    }
  }

  return (
    <label>
      Webhook secret {hasSecret ? "(set)" : "(not set)"}
      <input
        type="password"
        autoComplete="new-password"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
      />
      <button type="button" disabled={saving || draft === ""} onClick={save}>
        Replace
      </button>
    </label>
  );
}
