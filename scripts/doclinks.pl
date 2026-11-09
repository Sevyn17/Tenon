#!/usr/bin/env perl
# 文档相对链接检查：git 跟踪的 .md 里 [文字](目标) 的相对目标必须存在。
# 跳过围栏代码块、行内代码、外部链接（带协议）与页内锚点（#…）；不查锚点本身。
# 用法：在仓库根运行 `perl scripts/doclinks.pl [要跳过的目录前缀…]`，有坏链时退出码 1。
use strict;
use warnings;
use utf8;
use File::Basename qw(dirname);
use Encode qw(decode encode);

binmode STDOUT, ':encoding(UTF-8)';
my @skip = map { decode('UTF-8', $_) } @ARGV;

local $/ = "\0";
open(my $ls, '-|', 'git', '-c', 'core.quotepath=off', 'ls-files', '-z', '--', '*.md') or die "git ls-files: $!";
my @files = map { chomp; decode('UTF-8', $_) } <$ls>;
close $ls;
$/ = "\n";

my $bad = 0;
FILE: for my $f (@files) {
    for my $p (@skip) { next FILE if index($f, $p) == 0 }
    open(my $fh, '<:encoding(UTF-8)', encode('UTF-8', $f)) or next;
    my $dir = dirname($f);
    my ($fence, $n) = ('', 0);
    while (my $line = <$fh>) {
        $n++;
        if ($line =~ /^\s*(`{3,}|~{3,})/) {
            my $m = $1;
            if ($fence eq '') { $fence = substr($m, 0, 1) x length($m) }
            elsif (substr($m, 0, 1) eq substr($fence, 0, 1) && length($m) >= length($fence)) { $fence = '' }
            next;
        }
        next if $fence ne '';
        $line =~ s/(`+).*?\1//g;    # 行内代码
        while ($line =~ /\]\((<[^>]+>|[^)\s]+)\)/g) {
            my $t = $1;
            $t =~ s/^<(.*)>$/$1/;
            next if $t =~ /^[a-zA-Z][a-zA-Z0-9+.-]*:/ || $t =~ /^#/;
            $t =~ s/#.*$//;
            $t =~ s/%([0-9A-Fa-f]{2})/chr(hex($1))/ge if $t =~ /%[0-9A-Fa-f]{2}/;
            next if $t eq '';
            my $path = $t =~ m{^/} ? substr($t, 1) : "$dir/$t";
            next if -e encode('UTF-8', $path);
            print "  $f:$n → $t\n";
            $bad++;
        }
    }
    close $fh;
}
if ($bad) { print "坏链 $bad 处\n"; exit 1 }
