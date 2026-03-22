# -*- mode: ruby -*-
# vi: set ft=ruby :

Vagrant.configure("2") do |config|
  config.vm.synced_folder '.', '/vagrant', disabled: true
  config.vm.define "jagr-target" do |target|
    target.vm.box = "generic/ubuntu2204"
    target.vm.hostname = "jagr-target"

    target.vm.network "private_network", ip: '172.28.128.3'

    target.vm.provider "virtualbox" do |v|
      v.name = "jagr-target"
      v.memory = 4096
      v.cpu = 2
    end

    target.vm.provision "shell", path: "scripts/setup-vulns.sh"
  end
end
