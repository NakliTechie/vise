# B05 jq reference consumer.  Raw inputs are structurally scanned before
# fromjson so jq cannot hide duplicate keys or overflowing JSON numbers.

def bad: error("invalid");
def req($c): if $c then . else bad end;
def obj: type == "object";
def arr: type == "array";
def str: type == "string";
def int: type == "number" and isfinite and (floor == .);
def posint: int and . > 0;
def hash: str and test("^sha256:[0-9a-f]{64}$");
def revision: str and test("^[0-9a-f]{40}$");
def sorted_unique_strings:
  arr and all(.[]; str and length>0) and (. == (sort)) and (length == (unique|length));

# Return {i,key?}; indices are Unicode scalar indices (inputs were UTF-8 checked).
def ws($a;$i):
  if $i < ($a|length) and ([9,10,13,32]|index($a[$i])) != null
  then ws($a;$i+1) else $i end;
def hexval($c):
  if $c>=48 and $c<=57 then $c-48
  elif $c>=65 and $c<=70 then $c-55
  elif $c>=97 and $c<=102 then $c-87 else bad end;
def u4($a;$i):
  if $i+3 >= ($a|length) then bad else
    (hexval($a[$i])*4096 + hexval($a[$i+1])*256 +
     hexval($a[$i+2])*16 + hexval($a[$i+3])) end;
def scanstr($a;$i;$out):
  if $i >= ($a|length) then bad
  elif $a[$i] == 34 then {i:($i+1),key:($out|implode)}
  elif $a[$i] < 32 then bad
  elif $a[$i] != 92 then scanstr($a;$i+1;$out+[$a[$i]])
  elif $i+1 >= ($a|length) then bad
  elif $a[$i+1] == 117 then
    (u4($a;$i+2)) as $u |
    if $u>=55296 and $u<=56319 then
      if $i+11 >= ($a|length) or $a[$i+6]!=92 or $a[$i+7]!=117 then bad else
        (u4($a;$i+8)) as $v |
        if $v<56320 or $v>57343 then bad
        else scanstr($a;$i+12;$out+[65536+(($u-55296)*1024)+($v-56320)]) end
      end
    elif $u>=56320 and $u<=57343 then bad
    else scanstr($a;$i+6;$out+[$u]) end
  else
    ({"34":34,"92":92,"47":47,"98":8,"102":12,"110":10,"114":13,"116":9}
      | .[($a[$i+1]|tostring)]) as $e |
    if $e == null then bad else scanstr($a;$i+2;$out+[$e]) end
  end;
# Value strings do not need decoding for duplicate detection.  Keeping only an
# index makes scanning a maximum-sized stderr linear instead of repeatedly
# copying an ever-growing codepoint array.
def skipstr($a;$i):
  if $i >= ($a|length) then bad
  elif $a[$i] == 34 then $i+1
  elif $a[$i] < 32 then bad
  elif $a[$i] != 92 then skipstr($a;$i+1)
  elif $i+1 >= ($a|length) then bad
  elif $a[$i+1] == 117 then
    (u4($a;$i+2)) as $u |
    if $u>=55296 and $u<=56319 then
      if $i+11 >= ($a|length) or $a[$i+6]!=92 or $a[$i+7]!=117 then bad else
        (u4($a;$i+8)) as $v |
        if $v<56320 or $v>57343 then bad else skipstr($a;$i+12) end
      end
    elif $u>=56320 and $u<=57343 then bad else skipstr($a;$i+6) end
  elif ($a[$i+1]|IN(34,92,47,98,102,110,114,116)) then skipstr($a;$i+2)
  else bad end;
def digits($a;$i):
  if $i < ($a|length) and $a[$i]>=48 and $a[$i]<=57
  then digits($a;$i+1) else $i end;
def scannum($a;$i):
  ($i + (if $a[$i]==45 then 1 else 0 end)) as $s |
  (if $s>=($a|length) then bad
  elif $a[$s]==48 then ($s+1)
  elif $a[$s]>=49 and $a[$s]<=57 then digits($a;$s+1)
  else bad end) as $j |
  (if $j<($a|length) and $a[$j]==46 then
     (digits($a;$j+1)) as $k | if $k==$j+1 then bad else $k end
   else $j end) as $k |
  (if $k<($a|length) and ($a[$k]==69 or $a[$k]==101) then
     ($k+1 + (if $k+1<($a|length) and ($a[$k+1]==43 or $a[$k+1]==45) then 1 else 0 end)) as $p |
     (digits($a;$p)) as $q | if $q==$p then bad else $q end
   else $k end) as $z |
  # jq 1.7 preserves an overflowing decimal token but arithmetic saturates it.
  # Conversion through arithmetic exposes that saturation; max finite itself is OK.
  (($a[$i:$z]|implode)|tonumber) as $n |
  if ($n|isfinite) and (($n*1)|isfinite) then $z else bad end;
def scanvalue($a;$i;$depth):
  if $depth > 128 then bad else
  (ws($a;$i)) as $p |
  if $p>=($a|length) then bad
  elif $a[$p]==34 then {i:skipstr($a;$p+1)}
  elif $a[$p]==123 then
    ({i:($p+1),seen:{},done:false} |
     until(.done;
       (ws($a;.i)) as $q |
       if $q<($a|length) and $a[$q]==125 then .i=$q+1 | .done=true
       elif $q>=($a|length) or $a[$q]!=34 then bad
       else (scanstr($a;$q+1;[])) as $s |
         if .seen[$s.key] then bad else .seen[$s.key]=true |
           (ws($a;$s.i)) as $c | if $c>=($a|length) or $a[$c]!=58 then bad else
             (scanvalue($a;$c+1;$depth+1)) as $v | (ws($a;$v.i)) as $z |
             if $z>=($a|length) then bad
             elif $a[$z]==125 then .i=$z+1 | .done=true
             elif $a[$z]==44 then .i=$z+1
             else bad end
           end
         end
       end) | {i:.i})
  elif $a[$p]==91 then
    ({i:($p+1),done:false} |
     until(.done;
       (ws($a;.i)) as $q |
       if $q<($a|length) and $a[$q]==93 then .i=$q+1 | .done=true
       else (scanvalue($a;$q;$depth+1)) as $v | (ws($a;$v.i)) as $z |
         if $z>=($a|length) then bad
         elif $a[$z]==93 then .i=$z+1 | .done=true
         elif $a[$z]==44 then .i=$z+1
         else bad end
       end) | {i:.i})
  elif $a[$p]==116 and ($a[$p:$p+4]|implode)=="true" then {i:($p+4)}
  elif $a[$p]==102 and ($a[$p:$p+5]|implode)=="false" then {i:($p+5)}
  elif $a[$p]==110 and ($a[$p:$p+4]|implode)=="null" then {i:($p+4)}
  elif $a[$p]==45 or ($a[$p]>=48 and $a[$p]<=57) then {i:scannum($a;$p)}
  else bad end end;
def strictjson:
  explode as $a | (scanvalue($a;0;1)) as $v |
  if ws($a;$v.i) != ($a|length) then bad else fromjson end;

def binding_ok:
  obj and (.request_id|str and length>0) and
  (.cmd=="gate" or .cmd=="verify") and (.argv|arr) and
  (.root|str and startswith("/")) and
  (.candidate|hash) and (.manifest|hash) and (.lock|hash) and
  (.evaluator|hash) and (.environment|hash) and (.artifact|hash) and
  (.producer|obj and (.version|str and length>0) and (.revision|revision) and
    (.modified|type=="boolean") and ((has("built")|not) or (.built|str))) and
  (.scope|obj) and
  (.scope as $s |
    if $s.kind=="full" then
      ($s.probes|sorted_unique_strings and length>0) and
      ($s.metrics|sorted_unique_strings) and
      (all($s.probes[]; . as $id | all($s.metrics[]; . != $id))) and
      (.argv==[.cmd,"--json"])
    elif $s.kind=="probe" then
      ($s.probes|sorted_unique_strings and length==1) and
      ($s.metrics==[]) and
      ((.argv==[.cmd,"--json","--probe",$s.probes[0]]) or
       (.argv==[.cmd,"--probe",$s.probes[0],"--json"]))
    else false end);
def expected_ok:
  obj and .v==1 and (.binding|binding_ok) and
  (.trusted_producers|arr and length>0 and all(.[];
    obj and (.evaluator|hash) and (.revision|revision) and (.modified|type=="boolean"))) and
  (.max_elapsed_ms|posint) and (.max_stream_bytes|posint and .<=1048576);
def defined_binding:
  {request_id,cmd,argv,root,scope:(.scope|{kind,probes,metrics}),candidate,manifest,lock,evaluator,environment,artifact,
   producer:(.producer|{version,revision,modified} +
     (if has("built") then {built} else {} end))};
def same_binding($a;$b): ($a|defined_binding) == ($b|defined_binding);
def trusted($b;$e): any($e.trusted_producers[];
  .evaluator==$b.evaluator and .revision==$b.producer.revision and
  .modified==$b.producer.modified);

def all_count_fields($c):
  all(["declared","pass","behavior","flaky","harness","metric","unmet","skipped"][];
      . as $k | ($c|has($k)) and ($c[$k]|int and .>=0));
def classes_ok($x):
  ($x|arr and all(.[];str) and length==(unique|length) and
    all(.[];IN("harness","flake","behavior","unmet","metric")));
def failure_ok:
  . as $f | obj and (.class|IN("harness","flake","behavior","unmet","metric")) and
  ((has("detail")|not) or (.detail|str)) and
  ((has("diff")|not) or (.diff|str)) and
  ((has("operator")|not) or .operator==true) and
  ((has("usage")|not) or .usage==true) and
  (all(["expect","got"][]; . as $k | ($f|has($k)|not) or
    ($f[$k]|obj and
      ((has("exit")|not) or (.exit|int)) and
      ((has("stdout")|not) or (.stdout|hash)) and
      ((has("stderr")|not) or (.stderr|hash)) and
      ((has("files")|not) or (.files|obj and all(.[];hash))))));
def metric_ok:
  obj and (.base|type=="number" and isfinite) and
  (.now|type=="number" and isfinite) and (.delta|type=="number" and isfinite) and
  (.direction|IN("up","down")) and (.enforce|IN("none","no-regress"));
def pins_ok:
  . as $p | obj and (.evaluated|int and .>=0) and
  (.unmet|sorted_unique_strings and length<=3) and
  (.unmet_count|int and .>=0 and .>=($p.unmet|length)) and
  (.passing_unaccepted|sorted_unique_strings and length<=3) and
  (.passing_unaccepted_count|int and .>=0 and .>=($p.passing_unaccepted|length));
def mapping($x):
  if $x.exit==0 then $x.verdict=="green" and $x.next.action=="proceed"
  elif $x.exit==1 then $x.verdict=="red" and $x.next.action=="revert"
  elif $x.exit==2 then $x.verdict=="indeterminate" and ($x.next.action|IN("fix_probe","human","fix_invocation"))
  elif $x.exit==3 then $x.verdict=="indeterminate" and $x.next.action=="quarantine_ack"
  elif $x.exit==4 then $x.verdict=="indeterminate" and $x.next.action=="record_first"
  elif $x.exit==5 then $x.verdict=="red" and $x.next.action=="revert"
  elif $x.exit==6 then $x.verdict=="red" and $x.next.action=="build"
  else false end;
def disposition($e): if $e==0 then "proceed" elif $e==1 or $e==5 then "revert"
  elif $e==6 then "build" else "escalate" end;

def interpret($cap;$exp):
  ($cap|req(obj and .v==1 and (.binding|binding_ok) and (.binding_after|binding_ok) and
    (.termination|obj) and (.elapsed_ms|int and .>=0) and (.stdout|str) and (.stderr|str))) |
  ($exp|req(expected_ok)) |
  ($cap|req(same_binding(.binding;$exp.binding) and
    same_binding(.binding_after;$exp.binding) and same_binding(.binding;.binding_after) and
    trusted(.binding;$exp) and
    .elapsed_ms <= $exp.max_elapsed_ms and
    ((.stdout|utf8bytelength) <= $exp.max_stream_bytes) and
    ((.stderr|utf8bytelength) <= $exp.max_stream_bytes) and
    .termination.kind=="exit" and (.termination.code|int))) |
  ($cap.stdout|req(endswith("\n") and ([range(0;length-1)] as $dummy | true))) |
  ($cap.stdout[0:-1]|strictjson) as $r |
  ($r|req(obj and .v==1 and (.cmd==$cap.binding.cmd) and (.exit|int) and
    .exit==$cap.termination.code and (.next|obj and (.action|str) and (.detail|str)) and
    (.verdict|IN("green","red","indeterminate")) and (.counts|all_count_fields(.)) and
    ((has("classes")|not) or classes_ok(.classes)) and
    ((has("failures")|not) or (.failures|obj and all(.[];failure_ok))) and
    ((has("metrics")|not) or (.metrics|obj and all(.[];metric_ok))) and
    ((has("pins")|not) or (.pins|pins_ok)) and
    ((has("lock")|not) or (.lock|hash and .==$cap.binding.lock)) and mapping(.))) |
  ($r.classes // []) as $classes |
  ($r.failures // {}) as $failures |
  ([$failures|to_entries[]|select(.value.class=="unmet")|.key]|sort) as $unmet |
  ($r.pins // {evaluated:0,unmet:[],unmet_count:0,passing_unaccepted:[],passing_unaccepted_count:0}) as $pins |
  ($r|req(
    (all($failures[]; (.class=="harness") or
      (((has("operator") and .operator==true) or (has("usage") and .usage==true))|not))) and
    (.counts.harness==([$failures[]|select(.class=="harness")]|length)) and
    (.counts.flaky==([$failures[]|select(.class=="flake")]|length)) and
    (.counts.behavior==([$failures[]|select(.class=="behavior")]|length)) and
    (.counts.unmet==([$failures[]|select(.class=="unmet")]|length)) and
    (.counts.metric==([$failures[]|select(.class=="metric")]|length)) and
    (($classes|sort) == ([$failures[].class]|unique|sort)) and
    (if .exit==2 then
       ([ $failures[] | select(.class=="harness" and .usage==true) ]|length) as $uses |
       ([ $failures[] | select(.class=="harness" and .operator==true) ]|length) as $ops |
       (if $uses>0 then .next.action=="fix_invocation"
        elif $ops>0 then .next.action=="human" else .next.action=="fix_probe" end)
     else true end) and
    (if .exit==0 then ((has("classes")|not) and (has("failures")|not) and
       .counts.behavior==0 and .counts.flaky==0 and .counts.harness==0 and
       .counts.metric==0 and .counts.unmet==0 and .counts.skipped==0 and
       .counts.pass==.counts.declared) else true end) and
    (if (.exit|IN(0,1,3,4,5,6)) then
       .counts.declared==(($cap.binding.scope.probes|length)+($cap.binding.scope.metrics|length))
     else true end) and
    (if (.exit|IN(0,1,3,5,6)) then
       (.counts.pass + .counts.behavior + .counts.flaky + .counts.harness +
        .counts.metric + .counts.unmet + .counts.skipped)==.counts.declared
     else true end) and
    (if .exit==4 then
       .counts.pass==0 and .counts.skipped==.counts.declared and
       .counts.behavior==0 and .counts.flaky==0 and .counts.harness==0 and
       .counts.metric==0 and .counts.unmet==0 and
       (has("lock")|not) and (has("failures")|not) and (has("classes")|not) and
       (has("metrics")|not) and (has("pins")|not)
     else true end) and
    (if has("pins") then $pins.unmet_count==($unmet|length) and
       $pins.evaluated<=($cap.binding.scope.probes|length) and
       $pins.evaluated>=($pins.unmet_count+$pins.passing_unaccepted_count) and
       all($pins.unmet[]; . as $id | any($cap.binding.scope.probes[]; .==$id)) and
       all($pins.passing_unaccepted[]; . as $id | any($cap.binding.scope.probes[]; .==$id)) and
       all($pins.unmet[]; . as $id | any($unmet[]; .==$id)) and
       all($pins.unmet[]; . as $id | all($pins.passing_unaccepted[]; .!=$id))
     else ($unmet|length)==0 end) and
    (if .exit==6 then has("pins") and $classes==["unmet"] and .counts.unmet==($unmet|length) and
       $pins.unmet_count==($unmet|length)
     else true end) and
    (if (.exit|IN(0,1,3,5,6)) then has("lock") else true end))) |
  {reply:$r, unmet:$unmet, pins:$pins,
   decision:{v:1, disposition:disposition($r.exit), vise_exit:$r.exit,
     verdict:$r.verdict, next_action:$r.next.action, classes:($classes|sort),
     unmet_ids:$unmet, checks_skipped:$r.counts.skipped,
     metrics_skipped:(if $cap.binding.scope.kind=="probe" then 0
       elif $r.exit==4 then ($cap.binding.scope.metrics|length)
       elif $r.exit==2 then null else $r.counts.skipped end),
     metrics_checked:($cap.binding.scope.kind=="full" and ($r.exit==0 or $r.exit==5) and
       $r.counts.skipped==0),
     passing_unaccepted_ids:$pins.passing_unaccepted,
     passing_unaccepted_count:$pins.passing_unaccepted_count,
     operator_acceptance_required:($pins.passing_unaccepted_count!=0),
     binding:($cap.binding|defined_binding)}};

if $operation=="consume" then
  ($capture|strictjson) as $c | ($expected|strictjson) as $e |
  interpret($c;$e).decision
elif $operation=="progress" then
  ($previous|strictjson) as $p | ($current|strictjson) as $c | ($expected|strictjson) as $e |
  ($e|req(expected_ok and (.previous_binding|binding_ok) and
    (.lineage|obj and (.parent_candidate|hash) and (.child_candidate|hash)))) |
  (interpret($p;($e|.binding=.previous_binding))) as $pi |
  (interpret($c;$e)) as $ci |
  ($p.binding) as $pb | ($c.binding) as $cb |
  ($e|req(.lineage.parent_candidate==$pb.candidate and .lineage.child_candidate==$cb.candidate)) |
  ((($pb|defined_binding|del(.request_id,.candidate,.artifact))) ==
   (($cb|defined_binding|del(.request_id,.candidate,.artifact)))) as $stable |
  (($pb.request_id!=$cb.request_id) and ($pb.candidate!=$cb.candidate)) as $fresh |
  (($pi.reply.exit==6) and ($ci.reply.exit==6) and
   ($pi.reply.classes==["unmet"]) and ($ci.reply.classes==["unmet"]) and
   ($pb.cmd=="gate") and ($cb.cmd=="gate") and ($pb.scope.kind=="full") and
   ($cb.scope.kind=="full")) as $eligible |
  # Full construction accounting; every failure is one in-scope unmet probe.
  (($pi.reply.failures|length)==($pi.unmet|length) and
   ($ci.reply.failures|length)==($ci.unmet|length) and
   all($pi.unmet[]; IN($pb.scope.probes[])) and all($ci.unmet[]; IN($cb.scope.probes[])) and
   $pi.reply.counts.declared==(($pb.scope.probes|length)+($pb.scope.metrics|length)) and
   $ci.reply.counts.declared==(($cb.scope.probes|length)+($cb.scope.metrics|length)) and
   $pi.reply.counts.skipped==($pb.scope.metrics|length) and $ci.reply.counts.skipped==($cb.scope.metrics|length) and
   $pi.reply.counts.pass==(($pb.scope.probes|length)-($pi.unmet|length)) and
   $ci.reply.counts.pass==(($cb.scope.probes|length)-($ci.unmet|length))) as $accounted |
  (($ci.unmet|length)<($pi.unmet|length) and all($ci.unmet[]; IN($pi.unmet[]))) as $subset |
  {v:1,progress_allowed:($stable and $fresh and $eligible and $accounted and $subset),metrics_checked:false}
else bad end
