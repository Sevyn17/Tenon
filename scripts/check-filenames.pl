#!/usr/bin/env perl
# 从标准输入读取 NUL 分隔的 Git 路径，打印 Go 模块 zip 不接受的文件名。
# 用法：git ls-files -z | perl scripts/check-filenames.pl
use strict;
use warnings;
use utf8;

binmode STDIN, ':encoding(UTF-8)';
binmode STDOUT, ':encoding(UTF-8)';
local $/ = "\0";

while (my $path = <STDIN>) {
  chomp $path;
  for my $c (split //, $path) {
    if (ord($c) <128) {
      next if $c =~ m{[A-Za-z0-9!#$%&()+,\-.=@\[\]^_{}~ /]};
    } else {
      next if $c =~ /\p{L}/;
    }
    print "  $path\n";
    last;
  }
}
