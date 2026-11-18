#!/usr/bin/env bash
# Throwaway (S1): list the native methods of the JDK I/O boundary classes
# for one JDK, so hook-table drift between releases (decision #29) is a
# diff. Usage: javap-natives.sh <JAVA_HOME>
set -euo pipefail
readonly jh="${1:?usage: javap-natives.sh JAVA_HOME}"
readonly classes=(
  java.io.FileInputStream java.io.FileOutputStream java.io.RandomAccessFile
  java.io.FileDescriptor java.io.FileCleanable
  sun.nio.ch.FileDispatcherImpl sun.nio.ch.UnixFileDispatcherImpl
  sun.nio.ch.SocketDispatcher sun.nio.ch.UnixDispatcher sun.nio.ch.Net
  sun.nio.ch.IOUtil sun.nio.fs.UnixNativeDispatcher
)
for c in "${classes[@]}"; do
  echo "== $c"
  "$jh/bin/javap" -p "$c" 2>&1 | grep -E 'native|rror' || true
done
