#!/bin/sh
# The microservices scenario on the M8 trial's traced host (decision #37:
# "The harness starts the test workload's microservices scenario on iom2
# before the run"; internal/agenttrial Prep copies this file and the
# workload jar to ~/iormesh-trial/ and starts it in the background).
#
# One checkout per round, as magefiles/magefile_m4workload.go runs it:
# the servers first, leaves before their callers (billing, inventory,
# orders), on fixed ports so the task prompt can name them, then the
# gateway, whose checkout drives the round; every service exits once it
# is served. The loop repeats the round so the agent can deploy at any
# time and still see whole checkouts (segments rotate each minute, #16).
set -u
jar="$HOME/iormesh-trial/workload.jar"
while :; do
	d=$(mktemp -d)
	java -jar "$jar" microservices --role billing --dir "$d" --port 18214 &
	sleep 5
	java -jar "$jar" microservices --role inventory --dir "$d" --port 18215 &
	sleep 5
	java -jar "$jar" microservices --role orders --dir "$d" --port 18216 \
		--inventory-port 18215 --billing-port 18214 &
	sleep 5
	java -jar "$jar" microservices --role gateway --dir "$d" \
		--orders-port 18216 --inventory-port 18215
	wait
	rm -rf "$d"
	sleep 15
done
