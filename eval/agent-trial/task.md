# Task

I want to see how the services of a small Java application talk to
each other and what they do with files, with iormesh, and I want
answers to the questions at the end of this message. Install iormesh,
trace the application, build the mesh and answer from it. Work with me
as the iormesh skill below says. The user guide holds the path; the
files you can READ are listed below.

My answers to the skill's start questions, except the mode, which I
give when you ask:

1. **Controller:** the host `{{controller}}`. You do not run on it:
   each command you run, or I type for you, runs there, in my shell
   as my user. It is the machine the guide calls the laptop.
2. **Hosts to trace:** `{{traced}}`, with the same SSH user;
   `ssh {{traced}}` from `{{controller}}` works (the key and the host
   key are in place). Both hosts are Rocky Linux 9 and my user has
   sudo there without a password.
3. **What to trace on `{{traced}}`:** the test workload's
   microservices scenario, already running: four Java services,
   restarted in a loop about every half minute. billing listens on
   port 18214, inventory on 18215, orders on 18216, and the gateway is
   a client whose command line holds `--role gateway`. Trace one
   project per service, named billing, inventory, orders and gateway,
   with the Java agent (guide chapter 8), so Java methods and HTTP
   requests show.
4. **Where the release comes from:** `{{release_url}}` on
   `{{controller}}`, as a release page would serve it: the archives
   and `SHA256SUMS`. Install from the release archive (guide chapter
   3).
5. **Trace data:** yes, the output may go to your model provider. The
   hosts are throwaway test machines with synthetic data only. I
   have no signing key yet; its password is in
   `$IORMESH_MINISIGN_PASSWORD` on `{{controller}}`, for `keygen`
   and `sign` (guide chapter 3).

When the mesh is built and you can answer, send all your answers in
one final message, one `ANSWER <id>:` line per question, concise and
naming only what the question asks.
