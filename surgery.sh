#!/bin/zsh
BINARY="/Users/mojashi/repos/cc-editor-wails/build/bin/surgery"
PROJECT_ID="-$(pwd | sed 's|^/||' | sed 's|/|-|g')"
PROJECT_DIR="$HOME/.claude/projects/$PROJECT_ID"

if [[ ! -d "$PROJECT_DIR" ]]; then
  echo "ERROR: Project dir not found: $PROJECT_DIR" >&2
  exit 1
fi

TOKEN="SURGERY_$(uuidgen | tr -d '-' | head -c 12)"
echo "$TOKEN"

nohup zsh -c "
  PROJECT_DIR='$PROJECT_DIR'
  PROJECT_ID='$PROJECT_ID'
  TOKEN='$TOKEN'
  BINARY='$BINARY'

  for i in 1 2 3 4 5 6; do
    sleep 0.5
    JSONL=\$(grep -rl \"\$TOKEN\" \"\$PROJECT_DIR\"/*.jsonl 2>/dev/null | head -1)
    [[ -n \"\$JSONL\" ]] && break
  done

  if [[ -z \"\$JSONL\" ]]; then
    JSONL=\$(ls -t \"\$PROJECT_DIR\"/*.jsonl 2>/dev/null | head -1)
  fi

  SESSION_ID=\$(basename \"\$JSONL\" .jsonl)
  \"\$BINARY\" \"\$PROJECT_ID\" \"\$SESSION_ID\"
" &>/dev/null &
