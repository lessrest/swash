#!/usr/bin/env perl
#
# chat.pl - minimal LLM chat client built on Swash sessions
#
# Speaks to the Anthropic Messages API or the OpenAI Responses API. The
# conversation lives in Swash events, so it works with either the systemd or
# POSIX backend and needs no files. API requests and tool commands also run as
# Swash sessions, so `swash events` and `swash history` show everything.
#
# Usage:
#   chat.pl [--provider anthropic|openai] [--model NAME] new PROMPT
#   chat.pl prompt SESSION_ID PROMPT
#   chat.pl run-tool SESSION_ID
#   chat.pl continue SESSION_ID
#   chat.pl list
#
# Requires only core Perl modules plus the swash and curl executables.
use v5.36;

use Encode         qw(decode);
use File::Basename qw(basename);
use Getopt::Long   qw(GetOptionsFromArray);
use JSON::PP       ();

# $JSON speaks UTF-8 bytes (process boundaries); $TEXT speaks Perl strings
# (JSON nested inside already-decoded JSON, such as event messages).
my $JSON = JSON::PP->new->utf8->canonical->allow_nonref;
my $TEXT = JSON::PP->new->canonical->allow_nonref;

# The one tool the model may call. Providers translate this into their own
# tool schema; the command is only executed by an explicit `run-tool`.
my %SHELL_TOOL = (
    name        => 'shell',
    description => 'Propose one non-interactive shell command when running a '
        . 'command would help answer the user. The command will not run '
        . 'automatically: it is shown to the user for review and only runs '
        . 'if they invoke a separate command. It runs through bash -lc in '
        . 'the current working directory, with stdout and stderr returned '
        . 'afterward. Do not use this tool for explanations or when a '
        . 'command is unnecessary.',
    schema => {
        type       => 'object',
        properties => {
            command => {
                type        => 'string',
                description => 'The complete shell command to execute with bash -lc.',
            },
        },
        required             => ['command'],
        additionalProperties => JSON::PP::false,
    },
);

# Tool output goes into a single event message passed on the command line,
# so keep it well under the kernel's per-argument limit.
my $MAX_TOOL_OUTPUT = 64 * 1024;

# ---------------------------------------------------------------------------
# Swash - thin wrapper around the swash CLI.
#
# Every method maps to one swash command. Output is parsed, exit statuses are
# turned into exceptions, and no shell is ever involved, so arguments need no
# quoting.
# ---------------------------------------------------------------------------
package Swash {
    sub new ($class, %args) {
        return bless { bin => $args{bin} // 'swash' }, $class;
    }

    # Run swash with @args and return (stdout, exit status). Stderr passes
    # through to ours so swash's own diagnostics stay visible.
    sub _capture ($self, @args) {
        open my $fh, '-|', $self->{bin}, @args
            or die "cannot run $self->{bin}: $!\n";
        my $output = do { local $/; <$fh> // '' };
        close $fh;
        return ($output, $? >> 8);
    }

    sub _pairs ($map) {
        return map { "$_=$map->{$_}" } sort keys %{ $map // {} };
    }

    # Start a detached session and return its ID. Options:
    #   protocol => 'shell' | 'sse'
    #   tags     => { FIELD => value, ... }   journal fields on every entry
    #   env      => { NAME => value, ... }    exported to the child only
    sub start ($self, $command, %opts) {
        my @args = ('start');
        push @args, '--protocol', $opts{protocol} if $opts{protocol};
        push @args, map { ('--tag', $_) } _pairs($opts{tags});

        my %env = %{ $opts{env} // {} };
        local @ENV{ keys %env } = values %env;

        my ($output, $status) = $self->_capture(@args, '--', @$command);
        my ($id, $state) = split ' ', $output;
        die "failed to start swash session (exit status $status)\n"
            unless $status == 0 && ($state // '') eq 'started';
        return $id;
    }

    # Stream a session's output, calling $on_line for each line (without the
    # trailing newline). Returns the session's exit status.
    sub follow ($self, $id, $on_line) {
        open my $fh, '-|', $self->{bin}, 'follow', $id
            or die "cannot run $self->{bin}: $!\n";
        while (my $line = <$fh>) {
            chomp $line;
            $on_line->($line);
        }
        close $fh;
        return $? >> 8;
    }

    # Append a structured event to a session.
    sub emit ($self, $id, %event) {
        my @args = ('emit', $id, '--event', $event{event});
        push @args, '--message', $event{message} if defined $event{message};
        push @args, map { ('--field', $_) } _pairs($event{fields});
        my (undef, $status) = $self->_capture(@args);
        die "swash emit failed (exit status $status)\n" if $status;
        return;
    }

    # Query events; returns decoded {cursor, timestamp, message, fields}
    # records in journal order. Filters: session, event, fields => {...}.
    sub events ($self, %filter) {
        my @args = ('events', '--json');
        push @args, '--session', $filter{session} if defined $filter{session};
        push @args, '--event',   $filter{event}   if defined $filter{event};
        push @args, map { ('--field', $_) } _pairs($filter{fields});
        my ($output, $status) = $self->_capture(@args);
        die "swash events failed (exit status $status)\n" if $status;
        return map { $JSON->decode($_) } grep { length } split /\n/, $output;
    }
}

# ---------------------------------------------------------------------------
# Conversation - a chat transcript stored as Swash events.
#
# Each turn is one `chat-message` event whose message is JSON content and
# whose fields carry the role, provider, and model. Roles:
#   user       content is the prompt string
#   assistant  content is the provider's native output items, kept verbatim
#              so reasoning signatures and encrypted content replay intact
#   tool       content is {id, output, is_error} for one shell command
# ---------------------------------------------------------------------------
package Conversation {
    my $EVENT = 'chat-message';

    sub new ($class, $swash, $id) {
        return bless { swash => $swash, id => $id }, $class;
    }

    sub create ($class, $swash) {
        return $class->new($swash, _random_id());
    }

    sub _random_id {
        open my $fh, '<:raw', '/dev/urandom' or die "cannot open /dev/urandom: $!\n";
        read $fh, my $bytes, 12;
        return unpack 'H*', $bytes;
    }

    sub id ($self) { $self->{id} }

    sub turns ($self) {
        $self->{turns} //= [
            map {
                {
                    role    => $_->{fields}{CHAT_ROLE},
                    content => $TEXT->decode($_->{message}),
                    fields  => $_->{fields},
                }
            } $self->{swash}->events(session => $self->{id}, event => $EVENT)
        ];
        return @{ $self->{turns} };
    }

    sub exists ($self) { scalar $self->turns > 0 }

    sub last_turn ($self) { ($self->turns)[-1] }

    # The provider and model are recorded on every turn; the newest wins.
    sub provider_name ($self) { ($self->last_turn // {})->{fields}{CHAT_PROVIDER} }
    sub model         ($self) { ($self->last_turn // {})->{fields}{CHAT_MODEL} }

    sub append ($self, $role, $content, %fields) {
        $self->{swash}->emit(
            $self->{id},
            event   => $EVENT,
            message => $JSON->encode($content),
            fields  => { %fields, CHAT_ROLE => $role },
        );
        delete $self->{turns};
        return;
    }

    # The newest shell call the model made that has no recorded result yet.
    # Assistant content is provider-native, so the provider extracts calls.
    sub pending_tool_call ($self, $provider) {
        my @turns = $self->turns;
        my %answered = map { $_->{content}{id} => 1 } grep { $_->{role} eq 'tool' } @turns;
        my @pending = grep { $_->{name} eq $SHELL_TOOL{name} && !$answered{ $_->{id} } }
            map { $provider->tool_calls($_->{content}) }
            grep { $_->{role} eq 'assistant' } @turns;
        return $pending[-1];
    }

    # Summaries of every conversation, oldest first: {id, provider, model, prompt}.
    sub list ($class, $swash) {
        my (%seen, @summaries);
        for my $event ($swash->events(event => $EVENT, fields => { CHAT_ROLE => 'user' })) {
            my $id = $event->{fields}{SWASH_SESSION};
            next if $seen{$id}++;
            push @summaries, {
                id       => $id,
                provider => $event->{fields}{CHAT_PROVIDER} // '-',
                model    => $event->{fields}{CHAT_MODEL} // '-',
                prompt   => $TEXT->decode($event->{message}),
            };
        }
        return @summaries;
    }
}

# ---------------------------------------------------------------------------
# Providers - one package per API dialect. Each provides:
#   name, default_model, api_key_env, url, extra_headers
#   request_body($model, \@turns)  hashref to POST, streaming enabled
#   new_stream(on_text => sub)     parser for the SSE data payloads
#   tool_calls($assistant_content) list of {id, name, input}
#
# Streams share one interface: handle($event) consumes a decoded SSE payload,
# output() returns the native items to store, error() a failure message.
# ---------------------------------------------------------------------------
package Provider::Anthropic {
    sub new ($class) { bless {}, $class }

    sub name          { 'anthropic' }
    sub default_model { 'claude-opus-5' }
    sub api_key_env   { 'ANTHROPIC_API_KEY' }
    sub url           { 'https://api.anthropic.com/v1/messages' }
    sub extra_headers { ('anthropic-version: 2023-06-01') }

    sub request_body ($self, $model, $turns) {
        return {
            model      => $model,
            max_tokens => 4096,
            stream     => JSON::PP::true,
            tools      => [{
                name         => $SHELL_TOOL{name},
                description  => $SHELL_TOOL{description},
                strict       => JSON::PP::true,
                input_schema => $SHELL_TOOL{schema},
            }],
            tool_choice => { type => 'auto', disable_parallel_tool_use => JSON::PP::true },
            messages    => [ map { $self->_message($_) } @$turns ],
        };
    }

    sub _message ($self, $turn) {
        my ($role, $content) = @{$turn}{qw(role content)};
        return { role => 'user', content => $content } if $role eq 'user';
        return { role => 'assistant', content => $content } if $role eq 'assistant';
        return {
            role    => 'user',
            content => [{
                type        => 'tool_result',
                tool_use_id => $content->{id},
                content     => $content->{output},
                is_error    => $content->{is_error} ? JSON::PP::true : JSON::PP::false,
            }],
        };
    }

    sub tool_calls ($self, $blocks) {
        return map { { id => $_->{id}, name => $_->{name}, input => $_->{input} } }
            grep { $_->{type} eq 'tool_use' } @$blocks;
    }

    sub new_stream ($self, %args) { Provider::Anthropic::Stream->new(%args) }
}

package Provider::Anthropic::Stream {
    sub new ($class, %args) {
        return bless { on_text => $args{on_text}, output => [] }, $class;
    }

    sub output ($self) { $self->{output} }
    sub error  ($self) { $self->{error} }

    sub handle ($self, $event) {
        my $type = $event->{type} // '';

        if ($type eq 'content_block_start') {
            $self->{block}        = $event->{content_block};
            $self->{partial_json} = '';
        }
        elsif ($type eq 'content_block_delta') {
            my $block = $self->{block} or return;
            my $delta = $event->{delta} // {};
            my $kind  = $delta->{type} // '';
            if ($kind eq 'text_delta') {
                my $text = $delta->{text} // '';
                $block->{text} .= $text;
                $self->{on_text}->($text);
            }
            elsif ($kind eq 'input_json_delta') {
                $self->{partial_json} .= $delta->{partial_json} // '';
            }
            elsif ($kind eq 'thinking_delta') {
                $block->{thinking} .= $delta->{thinking} // '';
            }
            elsif ($kind eq 'signature_delta') {
                $block->{signature} .= $delta->{signature} // '';
            }
        }
        elsif ($type eq 'content_block_stop') {
            my $block = delete $self->{block} or return;
            if ($block->{type} eq 'tool_use') {
                $block->{input} = eval { $TEXT->decode($self->{partial_json} || '{}') }
                    // do { $self->{error} = 'invalid streamed tool input'; return };
            }
            push @{ $self->{output} }, $block;
        }
        elsif ($type eq 'error') {
            $self->{error} = $event->{error}{message} // 'unknown error';
        }
        return;
    }
}

package Provider::OpenAI {
    sub new ($class) { bless {}, $class }

    sub name          { 'openai' }
    sub default_model { 'gpt-5.6-sol' }
    sub api_key_env   { 'OPENAI_API_KEY' }
    sub url           { 'https://api.openai.com/v1/responses' }
    sub extra_headers { () }

    # Stateless use of the Responses API: nothing is stored server-side, and
    # reasoning comes back encrypted so the full output can be replayed.
    sub request_body ($self, $model, $turns) {
        return {
            model  => $model,
            stream => JSON::PP::true,
            store  => JSON::PP::false,
            include => ['reasoning.encrypted_content'],
            tools   => [{
                type        => 'function',
                name        => $SHELL_TOOL{name},
                description => $SHELL_TOOL{description},
                strict      => JSON::PP::true,
                parameters  => $SHELL_TOOL{schema},
            }],
            tool_choice         => 'auto',
            parallel_tool_calls => JSON::PP::false,
            input               => [ map { $self->_input_items($_) } @$turns ],
        };
    }

    sub _input_items ($self, $turn) {
        my ($role, $content) = @{$turn}{qw(role content)};
        return { role => 'user', content => $content } if $role eq 'user';
        return @$content if $role eq 'assistant';
        return {
            type    => 'function_call_output',
            call_id => $content->{id},
            output  => $content->{output},
        };
    }

    sub tool_calls ($self, $items) {
        return map {
            {
                id    => $_->{call_id},
                name  => $_->{name},
                input => $TEXT->decode($_->{arguments} || '{}'),
            }
        } grep { $_->{type} eq 'function_call' } @$items;
    }

    sub new_stream ($self, %args) { Provider::OpenAI::Stream->new(%args) }
}

package Provider::OpenAI::Stream {
    sub new ($class, %args) {
        return bless { on_text => $args{on_text}, output => [] }, $class;
    }

    sub output ($self) { $self->{output} }
    sub error  ($self) { $self->{error} }

    sub handle ($self, $event) {
        my $type = $event->{type} // '';

        if ($type eq 'response.output_text.delta') {
            $self->{on_text}->($event->{delta});
        }
        elsif ($type eq 'response.output_item.done') {
            push @{ $self->{output} }, $event->{item};
        }
        elsif ($type eq 'response.incomplete') {
            my $reason = $event->{response}{incomplete_details}{reason} // 'unknown reason';
            $self->{error} = "response incomplete ($reason)";
        }
        elsif ($type eq 'response.failed') {
            $self->{error} = $event->{response}{error}{message} // 'response failed';
        }
        elsif ($type eq 'error') {
            $self->{error} = $event->{message} // $event->{error}{message} // 'unknown error';
        }
        return;
    }
}

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------
package main;

my %PROVIDERS = map { $_->name => $_ } Provider::Anthropic->new, Provider::OpenAI->new;

my $PROGRAM = basename $0;

# curl runs inside a Swash session. The launcher script is static and the
# secret and request body travel through the environment, so the recorded
# SWASH_COMMAND shows only this script, the URL, and public headers.
my $CURL_LAUNCHER = <<'BASH';
url=$1; shift
curl --silent --show-error --no-buffer --fail-with-body --max-time 600 \
    -H "content-type: application/json" \
    -H "authorization: Bearer $CHAT_API_KEY" \
    --data-binary "$CHAT_REQUEST_BODY" \
    "$@" "$url"
status=$?
if ((status != 0)); then
    printf '\n\ndata: {"type":"error","error":{"message":"HTTP request failed (curl status %s)"}}\n\n' "$status"
fi
exit "$status"
BASH

sub usage {
    return <<"EOF";
Usage: $PROGRAM [options] new PROMPT
       $PROGRAM [options] prompt SESSION_ID PROMPT
       $PROGRAM run-tool SESSION_ID
       $PROGRAM [options] continue SESSION_ID
       $PROGRAM list

Options:
  --provider anthropic|openai   API to use. Defaults to the session's provider,
                                then \$CHAT_PROVIDER, then whichever of
                                ANTHROPIC_API_KEY / OPENAI_API_KEY is set.
  --model NAME                  Model. Defaults to the session's model, then
                                \$CHAT_MODEL, then the provider default
                                (claude-opus-5 or gpt-5.6-sol).

The model may request one shell command per response. The command is only
shown, not executed. run-tool executes the latest pending command through
Swash and records its result; continue sends that result back to the model.
EOF
}

sub have_executable ($name) {
    return scalar grep { -x "$_/$name" } grep { length } split /:/, $ENV{PATH} // '';
}

# Precedence: explicit option, then what the session already uses, then the
# environment, then a default. Assistant turns are stored in the provider's
# native format, so an existing session cannot change provider.
sub resolve_provider ($requested, $conversation) {
    my $stored = $conversation->exists ? $conversation->provider_name : undef;
    my $name   = $requested // $stored // $ENV{CHAT_PROVIDER}
        // ($ENV{ANTHROPIC_API_KEY} ? 'anthropic' : $ENV{OPENAI_API_KEY} ? 'openai' : 'anthropic');
    die "Unknown provider: $name (expected " . join(', ', sort keys %PROVIDERS) . ")\n"
        unless $PROVIDERS{$name};
    die "Session " . $conversation->id . " uses provider $stored; it cannot switch to $name\n"
        if defined $stored && $stored ne $name;
    return $PROVIDERS{$name};
}

sub resolve_model ($requested, $conversation, $provider) {
    return $requested
        // ($conversation->exists ? $conversation->model : undef)
        // $ENV{CHAT_MODEL}
        // $provider->default_model;
}

# Send the conversation to the model, stream the reply, and record it.
sub call_model ($swash, $conversation, $provider, $model) {
    my $api_key = $ENV{ $provider->api_key_env }
        or die $provider->api_key_env . " not set\n";
    my $body = $JSON->encode($provider->request_body($model, [ $conversation->turns ]));

    my $request = $swash->start(
        [ 'bash', '-c', $CURL_LAUNCHER, 'chat-request', $provider->url,
          map { ('-H', $_) } $provider->extra_headers ],
        protocol => 'sse',
        tags     => { CHAT_SESSION => $conversation->id },
        env      => {
            CHAT_API_KEY      => $api_key,
            CHAT_REQUEST_BODY => $body,
        },
    );

    my $printed = 0;
    my $stream  = $provider->new_stream(on_text => sub ($text) {
        return unless length($text // '');
        print $text;
        $printed = 1;
    });
    my @diagnostics;    # non-JSON lines, i.e. curl's stderr
    my $status = $swash->follow($request, sub ($line) {
        return unless length $line;
        my $event = eval { $JSON->decode($line) };
        return $stream->handle($event) if ref $event eq 'HASH';
        push @diagnostics, decode('UTF-8', $line);
    });
    print "\n" if $printed;

    if (my $error = $stream->error) {
        die join("\n", "API error: $error", @diagnostics) . "\n";
    }
    die join("\n", "Request failed (swash session $request exited $status)", @diagnostics) . "\n"
        if $status;

    my $output = $stream->output;
    if (@$output) {
        $conversation->append('assistant', $output, provider_fields($provider, $model));
    }
    for my $call (grep { $_->{name} eq $SHELL_TOOL{name} } $provider->tool_calls($output)) {
        say STDERR "\nShell tool requested:\n  " . ($call->{input}{command} // '');
        say STDERR "Run it with: $PROGRAM run-tool " . $conversation->id;
    }
    say STDERR 'Session: ' . $conversation->id;
    return;
}

sub provider_fields ($provider, $model) {
    return (CHAT_PROVIDER => $provider->name, CHAT_MODEL => $model);
}

# Execute the pending shell command as a Swash session and record the result.
sub run_tool ($swash, $conversation, $provider, $model) {
    my $call = $conversation->pending_tool_call($provider)
        or die 'No pending shell tool call in session: ' . $conversation->id . "\n";
    my $command = $call->{input}{command};
    die "Pending shell tool call has no command\n" unless length($command // '');

    say "+ $command";
    my $tool_session = $swash->start(
        [ 'bash', '-lc', $command ],
        tags => { CHAT_SESSION => $conversation->id, CHAT_TOOL_ID => $call->{id} },
    );

    my $output = '';
    my $status = $swash->follow($tool_session, sub ($line) {
        $line = decode('UTF-8', $line);
        say $line;
        $output .= "$line\n";
    });
    chomp $output;

    if (length $output > $MAX_TOOL_OUTPUT) {
        my $omitted = length($output) - $MAX_TOOL_OUTPUT;
        $output = substr($output, 0, $MAX_TOOL_OUTPUT)
            . "\n\n[output truncated: $omitted characters omitted]";
    }
    if (!length $output) {
        $output = $status == 0
            ? 'Command completed successfully with no output.'
            : 'Command produced no output.';
    }
    $output .= "\n\nCommand exited with status $status." if $status;

    $conversation->append(
        'tool',
        {
            id       => $call->{id},
            output   => $output,
            is_error => $status ? JSON::PP::true : JSON::PP::false,
        },
        provider_fields($provider, $model),
        CHAT_TOOL_ID        => $call->{id},
        CHAT_TOOL_SESSION   => $tool_session,
        CHAT_TOOL_EXIT_CODE => $status,
    );
    say STDERR "Tool result recorded. Continue with: $PROGRAM continue " . $conversation->id;
    return;
}

sub list_sessions ($swash) {
    my @sessions = Conversation->list($swash);
    splice @sessions, 0, -20 if @sessions > 20;
    say 'Recent sessions:';
    for my $session (@sessions) {
        my $prompt = $session->{prompt} =~ s/\s+/ /gr;
        $prompt = substr($prompt, 0, 57) . '...' if length $prompt > 60;
        printf "%s  %-9s %-16s %s\n", @{$session}{qw(id provider model)}, $prompt;
    }
    return;
}

sub main (@argv) {
    binmode $_, ':encoding(UTF-8)' for \*STDOUT, \*STDERR;
    STDOUT->autoflush(1);
    @argv = map { decode('UTF-8', $_) } @argv;

    my %opt;
    GetOptionsFromArray(\@argv, \%opt, 'provider=s', 'model=s', 'help|h')
        or die usage();
    if ($opt{help}) {
        print usage();
        return 0;
    }
    my ($command, @args) = @argv;
    $command //= '';

    if ($command eq 'help') {
        print usage();
        return 0;
    }
    if ($command !~ /^(?:new|prompt|run-tool|continue|list)$/) {
        print STDERR usage();
        return 2;
    }

    have_executable('swash') or die "swash is required\n";
    have_executable('curl')  or die "curl is required\n";
    my $swash = Swash->new;

    if ($command eq 'list') {
        die "list accepts no arguments\n" if @args;
        list_sessions($swash);
        return 0;
    }

    my $conversation;
    my $prompt;
    if ($command eq 'new') {
        die "Usage: $PROGRAM new PROMPT\n" unless @args == 1;
        ($prompt) = @args;
        $conversation = Conversation->create($swash);
    }
    else {
        my $expected = $command eq 'prompt' ? 'SESSION_ID PROMPT' : 'SESSION_ID';
        die "Usage: $PROGRAM $command $expected\n"
            unless @args == ($command eq 'prompt' ? 2 : 1);
        (my $id, $prompt) = @args;
        $conversation = Conversation->new($swash, $id);
        die "Session not found: $id\n" unless $conversation->exists;
    }

    my $provider = resolve_provider($opt{provider}, $conversation);
    my $model    = resolve_model($opt{model}, $conversation, $provider);

    if ($command eq 'run-tool') {
        run_tool($swash, $conversation, $provider, $model);
        return 0;
    }
    if ($command eq 'continue') {
        die 'No new tool result to send in session: ' . $conversation->id . "\n"
            unless ($conversation->last_turn // {})->{role} eq 'tool';
        call_model($swash, $conversation, $provider, $model);
        return 0;
    }
    if ($command eq 'prompt' && $conversation->pending_tool_call($provider)) {
        die "Run the pending tool first: $PROGRAM run-tool " . $conversation->id . "\n";
    }
    $conversation->append('user', $prompt, provider_fields($provider, $model));
    call_model($swash, $conversation, $provider, $model);
    return 0;
}

my $exit = eval { main(@ARGV) };
if (!defined $exit) {
    my $message = $@ =~ /\n\z/ ? $@ : "$@\n";
    print STDERR $message =~ /^Usage:/ ? $message : "error: $message";
    $exit = 1;
}
exit $exit;
