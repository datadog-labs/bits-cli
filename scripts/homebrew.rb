#!/usr/bin/env ruby
# Propose a verified release in a draft PR to datadog-labs/homebrew-pack.
# With --dry-run, preview the formula, destination, and PR without remote writes.

require "digest"
require "erb"
require "json"
require "open3"
require "rbconfig"
require "tmpdir"

REPO = "datadog-labs/bits-cli"
TAP = "datadog-labs/homebrew-pack"
PLATFORMS = %w[darwin_arm64 linux_amd64 linux_arm64].freeze
SRC_DIR = File.expand_path("..", __dir__)

# Keep stderr visible and pass arguments directly, without a shell.
def run(*args)
  output, status = Open3.capture2(*args)
  abort "ERROR: #{args.first} failed (#{status.exitstatus})" unless status.success?
  output.strip
end

def git_gh(*args)
  run("git", "-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential", *args)
end

def render(name, values)
  ERB.new(File.read(File.join(SRC_DIR, ".github/homebrew/#{name}.erb"))).result(binding)
end

def verified_checksum(path, checksums)
  entries = checksums.lines.map { |line| line.split }.select { |fields| fields[1] == File.basename(path) }
  unless entries.length == 1 && entries.first[0].match?(/\A[0-9a-fA-F]{64}\z/)
    abort "ERROR: expected exactly one SHA-256 checksum for #{File.basename(path)}"
  end
  checksum = entries.first[0].downcase
  abort "ERROR: checksum mismatch for #{File.basename(path)}" unless Digest::SHA256.file(path).hexdigest == checksum
  checksum
end

def main(args)
  dry_run = args.first == "--dry-run"
  args.shift if dry_run
  unless args.length == 1 && args.first.match?(/\Av(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\z/)
    abort "usage: #{$PROGRAM_NAME} [--dry-run] v<major>.<minor>.<patch>"
  end
  tag = args.first
  version = tag.delete_prefix("v")

  Dir.mktmpdir("bits-homebrew-") do |work_dir|
    release_dir = File.join(work_dir, "release")
    checksums_name = "bits_#{version}_checksums.txt"
    archives = PLATFORMS.to_h { |platform| [platform, "bits_#{version}_#{platform}.tar.gz"] }
    patterns = [checksums_name, *archives.values].flat_map { |name| ["--pattern", name] }
    run("gh", "release", "download", tag, "--repo", REPO, "--dir", release_dir, *patterns)
    checksums = File.read(File.join(release_dir, checksums_name))
    values = { "version" => version }
    archives.each do |platform, archive|
      path = File.join(release_dir, archive)
      values["sha256_#{platform}"] = verified_checksum(path, checksums)
      run("gh", "attestation", "verify", path, "--repo", REPO,
          "--signer-workflow", "#{REPO}/.github/workflows/release.yml", "--source-ref", "refs/heads/main")
    end
    warn "Verified the #{tag} archives."
    formula = render("bits.rb", values)
    body = render("pull-request.md", { "tag" => tag })

    tap_dir = File.join(work_dir, "homebrew-pack")
    # Clone full history so pushing to a stale fork never sends a shallow boundary.
    git_gh("clone", "--quiet", "https://github.com/#{TAP}.git", tap_dir)
    Dir.chdir(tap_dir) do
      if File.exist?("Formula/bits.rb")
        title = "Update bits to #{tag}"
        branch = "update/bits-#{version}"
      else
        title = "Add formula for bits #{version}"
        branch = "feature/add-bits-formula"
      end
      run("git", "switch", "--quiet", "--create", branch)
      File.write("Formula/bits.rb", formula)
      run(RbConfig.ruby, "-c", "Formula/bits.rb")
      run("git", "add", "Formula/bits.rb")
      if run("git", "diff", "--cached", "--name-only").empty?
        warn "The #{TAP} formula already installs bits #{tag}."
        return
      end
      # Push to a fork when the tap is not writable.
      repo = JSON.parse(run("gh", "api", "repos/#{TAP}"))
      if repo.dig("permissions", "push")
        push_repo = TAP
        head = branch
      else
        user = JSON.parse(run("gh", "api", "user")).fetch("login")
        find_fork = lambda do
          forks = JSON.parse(run("gh", "api", "repos/#{TAP}/forks", "--paginate", "--slurp")).flatten
          forks.find { |item| item.dig("owner", "login") == user }&.fetch("full_name")
        end
        push_repo = find_fork.call
        unless push_repo
          if dry_run
            push_repo = "#{user}/#{TAP.split('/').last}"
            warn "Dry run: would create fork #{push_repo}."
          else
            run("gh", "repo", "fork", TAP, "--clone=false", "--default-branch-only")
            5.times do |attempt|
              sleep 2 if attempt.positive?
              push_repo = find_fork.call
              break if push_repo
            end
            abort "ERROR: no fork of #{TAP} found for #{user}" unless push_repo
          end
        end
        head = "#{user}:#{branch}"
      end

      prs = JSON.parse(run("gh", "pr", "list", "--repo", TAP, "--head", branch,
                           "--author", "@me", "--state", "open", "--json", "url"))
      url = prs.first&.fetch("url")
      if dry_run
        warn "Dry run: would commit #{title.inspect} and push to #{push_repo}:#{branch}."
        puts run("git", "diff", "--cached", "--stat", "--patch")
        puts "\n#{url ? "Would update PR: #{url}" : "Would create draft PR in #{TAP}"}"
        puts "Base: #{TAP}:main\nHead: #{head}\nTitle: #{title}\n\n#{body}"
        return
      end
      run("git", "commit", "--quiet", "--message", title)
      git_gh("push", "--quiet", "--force", "https://github.com/#{push_repo}.git", "HEAD:refs/heads/#{branch}")

      if url
        run("gh", "pr", "edit", url, "--title", title, "--body", body)
      else
        url = run("gh", "pr", "create", "--repo", TAP, "--draft", "--base", "main",
                  "--head", head, "--title", title, "--body", body)
      end
      puts url
    end
  end
end

main(ARGV) if $PROGRAM_NAME == __FILE__
