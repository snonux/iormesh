#!/usr/bin/env bash
# Throwaway (S1, task wa4): builds bridge.jar (bootstrap-visible Hooks)
# and agent.jar (premain/agentmain + rewriter) into the given directory.
# Usage: build.sh <JAVA_HOME (25+)> <outdir>
set -euo pipefail
readonly jh="${1:?usage: build.sh JAVA_HOME OUTDIR}"
readonly out="${2:?outdir}"
here="$(cd "$(dirname "$0")" && pwd)"
readonly here
mkdir -p "$out/bridge" "$out/core"
mapfile -t bridge_src < <(find "$here/bridge" -name '*.java')
mapfile -t core_src < <(find "$here/core" -name '*.java')
"$jh/bin/javac" -d "$out/bridge" "${bridge_src[@]}"
"$jh/bin/jar" --create --file "$out/bridge.jar" -C "$out/bridge" .
"$jh/bin/javac" -d "$out/core" "${core_src[@]}"
cat > "$out/MANIFEST.MF" <<MF
Premain-Class: iormesh.s1.agent.Agent
Agent-Class: iormesh.s1.agent.Agent
Can-Retransform-Classes: true
Can-Redefine-Classes: true
MF
"$jh/bin/jar" --create --file "$out/agent.jar" --manifest "$out/MANIFEST.MF" -C "$out/core" .
