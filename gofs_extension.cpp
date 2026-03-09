#define DUCKDB_EXTENSION_MAIN

#include <duckdb.hpp>
#include <duckdb/common/exception.hpp>
#include <duckdb/common/string_util.hpp>
#include <duckdb/common/file_open_flags.hpp>
#include <duckdb/main/capi/capi_internal.hpp>
#include <gofs_extension.hpp>

extern "C" {
  int duckfs_directory_exists(int id, const char *path);

  int duckfs_file_exists(int id, const char *path);

  int duckfs_file_open(int id, const char *path);

  int duckfs_file_open_write(const char *path, int flags);

  int duckfs_file_close(int id);

  int64_t duckfs_file_size(int id);

  int64_t duckfs_file_read_at(int id, void *buf, int64_t size, int64_t off);

  int64_t duckfs_file_read(int id, void *buf, int64_t size);

  int64_t duckfs_file_write(int id, void *buf, int64_t size);

  int64_t duckfs_file_write_at(int id, void *buf, int64_t size, int64_t off);

  int64_t duckfs_file_seek(int id, int64_t off, int whence);

  int64_t duckfs_file_last_modified(int id);

  int duckfs_file_truncate(int id, int64_t size);

  int duckfs_file_sync(int id);

  int duckfs_file_is_on_disk(int id);

  int duckfs_create_directory(const char *path);

  int duckfs_remove_directory(const char *path);

  int duckfs_remove_file(const char *path);

  int duckfs_move_file(const char *source, const char *target);

  char *duckfs_glob(int id, const char *pattern);
}

// Compile-time checks to ensure our macro values match DuckDB's constants
static_assert(DUCKDB_FILE_FLAGS_READ == duckdb::FileOpenFlags::FILE_FLAGS_READ,
              "DUCKDB_FILE_FLAGS_READ mismatch");
static_assert(DUCKDB_FILE_FLAGS_WRITE == duckdb::FileOpenFlags::FILE_FLAGS_WRITE,
              "DUCKDB_FILE_FLAGS_WRITE mismatch");
static_assert(DUCKDB_FILE_FLAGS_FILE_CREATE == duckdb::FileOpenFlags::FILE_FLAGS_FILE_CREATE,
              "DUCKDB_FILE_FLAGS_FILE_CREATE mismatch");
static_assert(DUCKDB_FILE_FLAGS_FILE_CREATE_NEW == duckdb::FileOpenFlags::FILE_FLAGS_FILE_CREATE_NEW,
              "DUCKDB_FILE_FLAGS_FILE_CREATE_NEW mismatch");
static_assert(DUCKDB_FILE_FLAGS_APPEND == duckdb::FileOpenFlags::FILE_FLAGS_APPEND,
              "DUCKDB_FILE_FLAGS_APPEND mismatch");
static_assert(DUCKDB_FILE_FLAGS_NULL_IF_NOT_EXISTS == duckdb::FileOpenFlags::FILE_FLAGS_NULL_IF_NOT_EXISTS,
              "DUCKDB_FILE_FLAGS_NULL_IF_NOT_EXISTS mismatch");
static_assert(DUCKDB_FILE_FLAGS_EXCLUSIVE_CREATE == duckdb::FileOpenFlags::FILE_FLAGS_EXCLUSIVE_CREATE,
              "DUCKDB_FILE_FLAGS_EXCLUSIVE_CREATE mismatch");
static_assert(DUCKDB_FILE_FLAGS_NULL_IF_EXISTS == duckdb::FileOpenFlags::FILE_FLAGS_NULL_IF_EXISTS,
              "DUCKDB_FILE_FLAGS_NULL_IF_EXISTS mismatch");

namespace duckdb {
  constexpr const char *GOFS_FILESYSTEM_NAME = "GoFileSystem";
  constexpr int GOFS_SEEK_SET = 0;
  constexpr int GOFS_SEEK_CUR = 1;
  constexpr int GOFS_SEEK_END = 2;

  class GoFileHandle : public FileHandle {
    friend class GoFileSystem;
  public:
    GoFileHandle(FileSystem &fs, string path, FileOpenFlags flags, int id) :
      FileHandle(fs, path, flags),
      id(id) {
    }

    ~GoFileHandle() override {
      this->Close();
    }

    void Close() override {
      if (this->id >= 0) {
	duckfs_file_close(this->id);
	this->id = -1;
      }
    }

  private:
    int id;
  };

  class GoFileSystem : public FileSystem {
  public:
    GoFileSystem(int id) :
      FileSystem(),
      id(id) {
    }

    string GetName() const override {
      return GOFS_FILESYSTEM_NAME;
    }

    bool CanHandleFile(const string &) override {
      return true;
    }

    bool CanSeek() override {
      return true;
    }

    bool OnDiskFile(FileHandle &handle) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      return duckfs_file_is_on_disk(f->id) != 0;
    }

    bool DirectoryExists(const string &directory, optional_ptr<FileOpener> opener) override {
      return duckfs_directory_exists(this->id, directory.c_str());
    }

    bool FileExists(const string &filename, optional_ptr<FileOpener> opener) override {
      return duckfs_file_exists(this->id, filename.c_str());
    }

    vector<OpenFileInfo> Glob(const string &path, FileOpener *opener) override {
      char *result = duckfs_glob(this->id, path.c_str());
      if (!result) {
        // No glob characters in path; return as-is.
        return {path};
      }

      string results(result);
      free(result);

      if (results.empty()) {
        // Glob matched nothing.
        return {};
      }

      vector<OpenFileInfo> files;
      size_t pos = 0;
      while (pos < results.size()) {
        auto next = results.find('\n', pos);
        if (next == string::npos) {
          files.push_back(results.substr(pos));
          break;
        }
        files.push_back(results.substr(pos, next - pos));
        pos = next + 1;
      }
      return files;
    }

    unique_ptr<FileHandle> OpenFile(const string &path, FileOpenFlags flags, optional_ptr<FileOpener> opener) override {
      int id;
      // Check if we need write access
      if (flags.OpenForWriting()) {
        // Use the write-capable open function that uses os.OpenFile
        id = duckfs_file_open_write(path.c_str(), static_cast<int>(flags.GetFlagsInternal()));
      } else {
        // Use the read-only fs.FS based open
        id = duckfs_file_open(this->id, path.c_str());
      }

      if (id < 0) {
	// This appears to be the right way to report errors opening files,
	// usually indicating that the file does not exist. In several places,
	// it causes DuckDB to throw an exception indicating that a null pointer
	// was being dereferenced (e.g., when reading parquet files). These
	// errors are handled propertly in the C and Go bindings, and reported
	// to the callers as Go errors.
	return nullptr;
      }
      return make_uniq<GoFileHandle>(*this, path, flags, id);
    }

    int64_t GetFileSize(FileHandle &handle) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto size = duckfs_file_size(f->id);
      if (size < 0) {
	throw IOException("duckdb failed to get file size: "  + handle.GetPath());
      }
      return size;
    }

    void Read(FileHandle &handle, void *buf, int64_t size, idx_t off) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto n = duckfs_file_read_at(f->id, buf, size, off);
      if (n < 0) {
	throw IOException("duckdb failed to read file at location: " + handle.GetPath());
      }
      if (n < size) {
	throw IOException("duckdb read less than requested bytes: " + handle.GetPath());
      }
    }

    int64_t Read(FileHandle &handle, void *buf, int64_t size) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto n = duckfs_file_read(f->id, buf, size);
      if (n < 0) {
	throw IOException("duckdb failed to read file: " + handle.GetPath());
      }
      return n;
    }

    int64_t Write(FileHandle &handle, void *buf, int64_t size) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto n = duckfs_file_write(f->id, buf, size);
      if (n < 0) {
	throw IOException("duckdb failed to write file: " + handle.GetPath());
      }
      return n;
    }

    void Write(FileHandle &handle, void *buf, int64_t size, idx_t off) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto n = duckfs_file_write_at(f->id, buf, size, off);
      if (n < 0) {
	throw IOException("duckdb failed to write file at location: " + handle.GetPath());
      }
      if (n < size) {
	throw IOException("duckdb wrote less than requested bytes: " + handle.GetPath());
      }
    }

    void Truncate(FileHandle &handle, int64_t new_size) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      if (duckfs_file_truncate(f->id, new_size) < 0) {
	throw IOException("duckdb failed to truncate file: " + handle.GetPath());
      }
    }

    void FileSync(FileHandle &handle) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      if (duckfs_file_sync(f->id) < 0) {
	throw IOException("duckdb failed to sync file: " + handle.GetPath());
      }
    }

    void Seek(FileHandle &handle, idx_t off) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto n = duckfs_file_seek(f->id, off, GOFS_SEEK_SET);
      if (n < 0) {
	throw IOException("duckdb failed to seek to new file position: " + handle.GetPath());
      }
    }

    void Reset(FileHandle &handle) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto n = duckfs_file_seek(f->id, 0, GOFS_SEEK_SET);
      if (n < 0) {
	throw IOException("duckdb failed to reset file seek position: " + handle.GetPath());
      }
    }

    idx_t SeekPosition(FileHandle &handle) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto n = duckfs_file_seek(f->id, 0, GOFS_SEEK_CUR);
      if (n < 0) {
	throw IOException("duckdb failed get current file seek position: " + handle.GetPath());
      }
      return n;
    }

    timestamp_t GetLastModifiedTime(FileHandle &handle) override {
      auto unix_time = duckfs_file_last_modified(dynamic_cast<GoFileHandle*>(&handle)->id);
      return Timestamp::FromEpochSeconds(unix_time);
    }

    unique_ptr<FileHandle> OpenCompressedFile(QueryContext context, unique_ptr<FileHandle> handle, bool write) override {
      throw NotImplementedException("GoFileSystem does not support compressed files");
    }

    bool SubSystemIsDisabled(const string &name) override {
      return false;
    }

    void CreateDirectory(const string &directory, optional_ptr<FileOpener> opener) override {
      if (duckfs_create_directory(directory.c_str()) < 0) {
        throw IOException("GoFileSystem: failed to create directory: " + directory);
      }
    }

    void RemoveDirectory(const string &directory, optional_ptr<FileOpener> opener) override {
      if (duckfs_remove_directory(directory.c_str()) < 0) {
        throw IOException("GoFileSystem: failed to remove directory: " + directory);
      }
    }

    void RemoveFile(const string &filename, optional_ptr<FileOpener> opener) override {
      if (duckfs_remove_file(filename.c_str()) < 0) {
        throw IOException("GoFileSystem: failed to remove file: " + filename);
      }
    }

    void MoveFile(const string &source, const string &target, optional_ptr<FileOpener> opener) override {
      if (duckfs_move_file(source.c_str(), target.c_str()) < 0) {
        throw IOException("GoFileSystem: failed to move file from " + source + " to " + target);
      }
    }

  private:
    int id;
  };

  static void LoadInternal(DatabaseInstance &instance) {
  }

  void GoFSExtension::Load(ExtensionLoader &loader) {
    LoadInternal(loader.GetDatabaseInstance());
  }

  string GoFSExtension::Name() {
    return "gofs";
  }

  string GoFSExtension::Version() const {
    return "";
  }
}

extern "C" {
  DUCKDB_EXTENSION_API void gofs_init(duckdb::DatabaseInstance &db) {
    auto extension = duckdb::make_uniq<duckdb::GoFSExtension>();
    duckdb::ExtensionLoader loader(db, extension->Name());
    extension->Load(loader);
  }

  DUCKDB_EXTENSION_API const char *gofs_version() {
    return duckdb::DuckDB::LibraryVersion();
  }

  duckdb_state duckfs_register_subsystem(duckdb_database database, int id) {
    if (!database || id < 0) {
      return DuckDBError;
    }
    auto wrapper = reinterpret_cast<duckdb::DatabaseWrapper *>(database);
    try {
      auto &fs = duckdb::FileSystem::GetFileSystem(*wrapper->database->instance.get());
      fs.RegisterSubSystem(duckdb::make_uniq<duckdb::GoFileSystem>(id));
    } catch (...) {
      return DuckDBError;
    }
    return DuckDBSuccess;
  }

  duckdb_state duckfs_unregister_subsystem(duckdb_database database) {
    if (!database) {
      return DuckDBError;
    }
    auto wrapper = reinterpret_cast<duckdb::DatabaseWrapper *>(database);
    try {
      auto &fs = duckdb::FileSystem::GetFileSystem(*wrapper->database->instance.get());
      fs.UnregisterSubSystem(duckdb::GOFS_FILESYSTEM_NAME);
    } catch (...) {
      return DuckDBError;
    }
    return DuckDBSuccess;
  }
}

#ifndef DUCKDB_EXTENSION_MAIN
#error DUCKDB_EXTENSION_MAIN not defined
#endif
