packer {
  required_plugins {
    qemu = { version = "= 1.1.7", source = "github.com/hashicorp/qemu" }
  }
}

variable "base_image" { type = string }
variable "base_checksum" { type = string }
variable "output_dir" { type = string }
variable "ssh_key" { type = string }
variable "ssh_port" { type = number }
variable "proxy_port" { type = number }
variable "seed_files" { type = list(string) }
variable "provision" { type = list(string) }

source "qemu" "debian" {
  iso_url              = var.base_image
  iso_checksum         = var.base_checksum
  disk_image           = true
  output_directory     = var.output_dir
  vm_name              = "debian13.qcow2"
  format               = "qcow2"
  disk_size            = "10G"
  accelerator          = "kvm"
  machine_type         = "q35"
  cpu_model            = "host"
  headless             = true
  memory               = 2048
  cpus                 = 2
  net_device           = "virtio-net"
  disk_interface       = "virtio"
  communicator         = "ssh"
  ssh_username         = "agent"
  ssh_private_key_file = var.ssh_key
  ssh_timeout          = "5m"
  host_port_min        = var.ssh_port
  host_port_max        = var.ssh_port
  ssh_remote_tunnels   = ["3128:127.0.0.1:${var.proxy_port}"]
  cd_files             = var.seed_files
  cd_label             = "cidata"
  shutdown_command     = "sudo shutdown -P now"
  qemuargs             = [["-netdev", "user,id=user.0,restrict=on,hostfwd=tcp:127.0.0.1:${var.ssh_port}-:22"]]
}

build {
  sources = ["source.qemu.debian"]

  provisioner "shell" {
    inline_shebang   = "/bin/bash -e"
    environment_vars = [
      "http_proxy=http://127.0.0.1:3128",
      "https_proxy=http://127.0.0.1:3128",
      "HTTP_PROXY=http://127.0.0.1:3128",
      "HTTPS_PROXY=http://127.0.0.1:3128",
    ]
    inline = var.provision
  }
}
